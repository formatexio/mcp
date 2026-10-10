package app

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// This file implements the small OAuth 2.1 authorization server that lets
// remote MCP clients (Claude.ai, Claude Code, Cursor, ...) obtain a FormaTeX
// API key through a browser consent page:
//
//	GET  /.well-known/oauth-protected-resource[/mcp]  RFC 9728 resource metadata
//	GET  /.well-known/oauth-authorization-server      RFC 8414 server metadata
//	POST /register                                    RFC 7591 dynamic client registration
//	GET  /authorize, POST /authorize                  consent page, issues a one-time code
//	POST /token                                       code + PKCE S256 for an access token
//
// Client registrations are stateless: the client_id is a signed, self-contained
// token (see clientid.go), so they survive restarts and redeploys as long as
// the signing secret is stable. Pending authorization codes live in memory for
// a few minutes and are bounded. The POST endpoints are rate limited per client
// IP (see ratelimit.go).

const (
	codeTTL = 5 * time.Minute

	// clientTTL is how long a registration (client_id) stays valid. Nothing is
	// stored per client, so a long lifetime costs nothing; a short one would
	// strand users whose client cached an id and re-authorizes later.
	clientTTL = 365 * 24 * time.Hour

	// maxCodes bounds memory: authorization is unauthenticated until the form
	// is posted, so anyone can create entries. When the map is full the entry
	// closest to expiry is evicted, so new connections keep working under abuse.
	maxCodes = 10000

	// sweepInterval is the minimum time between full scans for expired entries.
	sweepInterval = time.Minute

	// Redirect URIs travel inside the client_id, so their count and length are
	// capped well below maxClientIDLen.
	maxRedirectURIs    = 5
	maxRedirectURILen  = 512
	maxClientNameRunes = 64
	maxStateLen        = 1024
	maxAPIKeyLen       = 512
	maxBodyBytes       = 64 << 10

	defaultClientName = "an AI client"
	apiKeysURL        = "https://formatex.io/dashboard/api-keys"
)

// registeredClient is a dynamically registered OAuth client (RFC 7591).
type registeredClient struct {
	name         string
	redirectURIs []string
	expiry       time.Time
}

// pendingCode is an issued authorization code waiting for exchange.
type pendingCode struct {
	clientID      string
	apiKey        string
	codeChallenge string
	redirectURI   string
	expiry        time.Time
}

// oauthServer holds the authorization server state. baseURL is the public
// origin of this MCP server without a trailing slash; it is also the issuer.
// secret signs client_ids; see loadOAuthSecret.
type oauthServer struct {
	baseURL string
	secret  []byte
	now     func() time.Time
	limiter *rateLimiter

	mu        sync.Mutex
	codes     map[string]pendingCode
	lastSweep time.Time
}

func newOAuthServer(baseURL string, secret []byte) *oauthServer {
	return &oauthServer{
		baseURL: strings.TrimRight(baseURL, "/"),
		secret:  secret,
		now:     time.Now,
		limiter: newRateLimiter(rateLimitRequests, rateLimitWindow),
		codes:   make(map[string]pendingCode),
	}
}

// registerRoutes mounts the OAuth endpoints on mux.
func (s *oauthServer) registerRoutes(mux *http.ServeMux) {
	prm := protectedResourceMetadataHandler(s.baseURL)
	mux.HandleFunc("/.well-known/oauth-protected-resource", prm)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", prm)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.metadataHandler)
	mux.HandleFunc("/register", s.rateLimited("register", false, s.registerHandler))
	mux.HandleFunc("/authorize", s.rateLimited("authorize", true, s.authorizeHandler))
	mux.HandleFunc("/token", s.rateLimited("token", false, s.tokenHandler))
}

// resourceURL is the canonical URI of the protected MCP endpoint.
func (s *oauthServer) resourceURL() string { return s.baseURL + "/mcp" }

// --- shared helpers --------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOAuthError writes an RFC 6749 section 5.2 error response.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
}

func setCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, MCP-Protocol-Version")
	h.Set("Access-Control-Max-Age", "86400")
}

// serveMetadata serves a public JSON metadata document for GET/HEAD with CORS
// so browser-based clients can discover the server.
func serveMetadata(w http.ResponseWriter, r *http.Request, doc any) {
	setCORS(w)
	switch r.Method {
	case http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet, http.MethodHead:
		writeJSON(w, http.StatusOK, doc)
	default:
		methodNotAllowed(w, "GET, HEAD, OPTIONS")
	}
}

// randomToken returns n random bytes as unpadded base64url.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// verifyPKCE checks an S256 code_verifier against the stored code_challenge.
func verifyPKCE(verifier, challenge string) bool {
	h := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

var challengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// --- metadata --------------------------------------------------------------

// protectedResourceMetadataHandler serves RFC 9728 OAuth 2.0 Protected
// Resource Metadata. It tells MCP clients which authorization server issues
// tokens for the /mcp endpoint.
func protectedResourceMetadataHandler(baseURL string) http.HandlerFunc {
	baseURL = strings.TrimRight(baseURL, "/")
	doc := map[string]any{
		"resource":                 baseURL + "/mcp",
		"authorization_servers":    []string{baseURL},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "FormaTeX MCP",
	}
	return func(w http.ResponseWriter, r *http.Request) {
		serveMetadata(w, r, doc)
	}
}

// metadataHandler serves RFC 8414 Authorization Server Metadata.
func (s *oauthServer) metadataHandler(w http.ResponseWriter, r *http.Request) {
	serveMetadata(w, r, map[string]any{
		"issuer":                                s.baseURL,
		"authorization_endpoint":                s.baseURL + "/authorize",
		"token_endpoint":                        s.baseURL + "/token",
		"registration_endpoint":                 s.baseURL + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		// RFC 9207: authorization responses carry an iss parameter.
		"authorization_response_iss_parameter_supported": true,
	})
}

// --- store -----------------------------------------------------------------

// sweepLocked drops expired codes. It scans at most once per sweepInterval so
// a burst of requests does not turn into a burst of scans. Callers must hold
// s.mu.
func (s *oauthServer) sweepLocked(now time.Time) {
	if now.Sub(s.lastSweep) < sweepInterval {
		return
	}
	s.lastSweep = now
	for k, v := range s.codes {
		if now.After(v.expiry) {
			delete(s.codes, k)
		}
	}
}

// evictEarliest removes the entry that expires first.
func evictEarliest[V any](m map[string]V, expiry func(V) time.Time) {
	var (
		oldestKey string
		oldest    time.Time
		found     bool
	)
	for k, v := range m {
		if e := expiry(v); !found || e.Before(oldest) {
			oldestKey, oldest, found = k, e, true
		}
	}
	if found {
		delete(m, oldestKey)
	}
}

func (s *oauthServer) addCode(p pendingCode) (string, error) {
	code, err := randomToken(24)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.sweepLocked(now)
	if len(s.codes) >= maxCodes {
		evictEarliest(s.codes, func(pc pendingCode) time.Time { return pc.expiry })
	}
	p.expiry = now.Add(codeTTL)
	s.codes[code] = p
	return code, nil
}

// consumeCode removes and returns a pending code. Codes are single use: the
// entry is deleted whether or not the rest of the exchange succeeds.
func (s *oauthServer) consumeCode(code string) (pendingCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.codes[code]
	if ok {
		delete(s.codes, code)
	}
	if !ok || s.now().After(p.expiry) {
		return pendingCode{}, false
	}
	return p, true
}

// --- redirect URI validation -----------------------------------------------

var (
	customSchemeRE = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

	// forbiddenSchemes can never be a redirect target, even as a private-use
	// scheme: they execute script, read local content or are network schemes
	// that are not a native app hand-off.
	forbiddenSchemes = map[string]bool{
		"javascript":  true,
		"data":        true,
		"file":        true,
		"vbscript":    true,
		"about":       true,
		"blob":        true,
		"view-source": true,
		"filesystem":  true,
		"ftp":         true,
		"ws":          true,
		"wss":         true,
	}

	// loopbackHosts may use plain http (RFC 8252 section 7.3) on any port.
	loopbackHosts = map[string]bool{
		"localhost": true,
		"127.0.0.1": true,
		"::1":       true,
	}
)

// validateRedirectURI accepts https URIs, http URIs on loopback hosts, and
// private-use custom schemes for native apps (RFC 8252). Fragments and
// userinfo are rejected, as are non-ASCII and control characters.
func validateRedirectURI(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("redirect URI is empty")
	}
	if len(raw) > maxRedirectURILen {
		return nil, errors.New("redirect URI is too long")
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return nil, errors.New("redirect URI contains whitespace, control, backslash or non-ASCII characters")
		}
	}
	if strings.Contains(raw, "#") {
		return nil, errors.New("redirect URI must not contain a fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("redirect URI is not a valid URI")
	}
	if u.Scheme == "" {
		return nil, errors.New("redirect URI must be absolute")
	}
	if u.User != nil {
		return nil, errors.New("redirect URI must not contain userinfo")
	}

	switch u.Scheme {
	case "https":
		if u.Hostname() == "" {
			return nil, errors.New("https redirect URI needs a host")
		}
	case "http":
		if !loopbackHosts[strings.ToLower(u.Hostname())] {
			return nil, errors.New("http redirect URIs are only allowed for localhost, 127.0.0.1 and [::1]")
		}
	default:
		if forbiddenSchemes[u.Scheme] || !customSchemeRE.MatchString(u.Scheme) {
			return nil, errors.New("redirect URI scheme is not allowed")
		}
		if u.Opaque == "" && u.Host == "" && u.Path == "" {
			return nil, errors.New("redirect URI is missing its target")
		}
	}
	return u, nil
}

// redirectDisplay describes where the user is about to be sent, for the
// consent page.
func redirectDisplay(u *url.URL) string {
	switch u.Scheme {
	case "https":
		return u.Host
	case "http":
		return u.Host + " (an app on this computer)"
	default:
		target := u.Scheme + ":"
		if u.Host != "" {
			target = u.Scheme + "://" + u.Host
		}
		return target + " (an app on this computer)"
	}
}

// sanitizeClientName makes an attacker-supplied display name safe to show:
// control and invisible formatting characters (including bidi overrides) are
// dropped, whitespace is collapsed and the length is capped.
func sanitizeClientName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == utf8.RuneError:
			return -1
		}
		return r
	}, name)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if utf8.RuneCountInString(cleaned) > maxClientNameRunes {
		cleaned = string([]rune(cleaned)[:maxClientNameRunes])
	}
	return cleaned
}

// --- registration ----------------------------------------------------------

type registrationRequest struct {
	ClientName    string   `json:"client_name"`
	RedirectURIs  []string `json:"redirect_uris"`
	GrantTypes    []string `json:"grant_types"`
	ResponseTypes []string `json:"response_types"`
}

// registerHandler implements RFC 7591 dynamic client registration for public
// clients. The client_id it returns is a signed token carrying the validated
// redirect URIs and name, so nothing is stored server side.
func (s *oauthServer) registerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}

	var req registrationRequest
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "request body must be a JSON object of client metadata")
		return
	}

	// Only the authorization code flow is offered. Reject registrations that
	// cannot work with it instead of pretending to support them.
	if len(req.GrantTypes) > 0 && !slices.Contains(req.GrantTypes, "authorization_code") {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "grant_types must include authorization_code")
		return
	}
	if len(req.ResponseTypes) > 0 && !slices.Contains(req.ResponseTypes, "code") {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "response_types must include code")
		return
	}

	if len(req.RedirectURIs) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required and must not be empty")
		return
	}
	if len(req.RedirectURIs) > maxRedirectURIs {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "too many redirect_uris")
		return
	}

	var (
		valid     []string
		firstProb error
	)
	for _, raw := range req.RedirectURIs {
		if _, err := validateRedirectURI(raw); err != nil {
			if firstProb == nil {
				firstProb = err
			}
			continue
		}
		if !slices.Contains(valid, raw) {
			valid = append(valid, raw)
		}
	}
	if len(valid) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", firstProb.Error())
		return
	}

	name := sanitizeClientName(req.ClientName)
	id, err := s.signClient(registeredClient{name: name, redirectURIs: valid})
	switch {
	case errors.Is(err, errClientTooLarge):
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", err.Error())
		return
	case err != nil:
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not create client registration")
		return
	}

	resp := map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        s.now().Unix(),
		"redirect_uris":              valid,
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	if name != "" {
		resp["client_name"] = name
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, resp)
}

// --- authorization ---------------------------------------------------------

// authRequest is a validated authorization request.
type authRequest struct {
	clientID      string
	client        registeredClient
	redirectURI   string
	redirectURL   *url.URL
	state         string
	codeChallenge string
}

// authFailure describes why an authorization request was refused. When fatal
// is set, client_id or redirect_uri could not be trusted, so the user sees an
// error page and nothing is redirected (RFC 6749 section 4.1.2.1). Otherwise
// the error is reported to the validated redirect URI.
type authFailure struct {
	fatal       bool
	code        string
	description string
}

// resourceAllowed reports whether an RFC 8707 resource indicator names this
// server. The origin and the /mcp endpoint are both accepted.
func (s *oauthServer) resourceAllowed(resource string) bool {
	r := strings.TrimSuffix(resource, "/")
	return r == s.baseURL || r == s.resourceURL()
}

// validateAuthorize checks an authorization request. On a non-fatal failure
// the returned authRequest still carries the validated redirect target.
func (s *oauthServer) validateAuthorize(v url.Values) (*authRequest, *authFailure) {
	fatal := func(msg string) (*authRequest, *authFailure) {
		return nil, &authFailure{fatal: true, code: "invalid_request", description: msg}
	}

	if len(v["client_id"]) > 1 || len(v["redirect_uri"]) > 1 {
		return fatal("The request repeats a parameter that must appear once.")
	}
	clientID := v.Get("client_id")
	if clientID == "" {
		return fatal("The request is missing a client_id.")
	}
	client, ok := s.lookupClient(clientID)
	if !ok {
		return fatal("This client registration is not valid for this server or has expired. Remove the FormaTeX connector from your AI client and add it again to register a new one.")
	}

	redirectURI := v.Get("redirect_uri")
	switch {
	case redirectURI == "" && len(client.redirectURIs) == 1:
		redirectURI = client.redirectURIs[0]
	case redirectURI == "":
		return fatal("The request is missing a redirect_uri.")
	case !slices.Contains(client.redirectURIs, redirectURI):
		return fatal("The redirect_uri does not match the one this client registered.")
	}
	redirectURL, err := url.Parse(redirectURI)
	if err != nil {
		return fatal("The redirect_uri is not valid.")
	}

	req := &authRequest{
		clientID:    clientID,
		client:      client,
		redirectURI: redirectURI,
		redirectURL: redirectURL,
		state:       v.Get("state"),
	}
	redirectable := func(code, msg string) (*authRequest, *authFailure) {
		return req, &authFailure{code: code, description: msg}
	}

	if len(req.state) > maxStateLen {
		req.state = ""
		return redirectable("invalid_request", "state is too long")
	}
	switch got := v["response_type"]; {
	case len(got) == 0:
		return redirectable("invalid_request", "response_type is required")
	case len(got) != 1 || got[0] != "code":
		return redirectable("unsupported_response_type", "response_type must be code")
	}
	if got := v["code_challenge_method"]; len(got) != 1 || got[0] != "S256" {
		return redirectable("invalid_request", "code_challenge_method must be S256")
	}
	challenge := v.Get("code_challenge")
	if len(v["code_challenge"]) != 1 || !challengeRE.MatchString(challenge) {
		return redirectable("invalid_request", "code_challenge is required and must be a base64url SHA-256 digest")
	}
	req.codeChallenge = challenge

	for _, res := range v["resource"] {
		if !s.resourceAllowed(res) {
			return redirectable("invalid_target", "resource must be "+s.resourceURL())
		}
	}
	return req, nil
}

// redirectResult sends the user agent back to the client with the given
// authorization response parameters plus iss (RFC 9207).
func (s *oauthServer) redirectResult(w http.ResponseWriter, r *http.Request, req *authRequest, params map[string]string) {
	target := *req.redirectURL
	q := target.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	if req.state != "" {
		q.Set("state", req.state)
	}
	q.Set("iss", s.baseURL)
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// authorizePage is the data for the consent and error page.
type authorizePage struct {
	// Consent form.
	ClientName      string
	RedirectDisplay string
	ClientID        string
	RedirectURI     string
	State           string
	CodeChallenge   string
	Resource        string
	FormError       string
	DescribedBy     string
	APIKeysURL      string

	// Dead-end error page. When FatalTitle is set no form is rendered.
	FatalTitle   string
	FatalMessage string
}

var authorizeTmpl = template.Must(template.New("authorize").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="robots" content="noindex">
  <title>{{if .FatalTitle}}{{.FatalTitle}}{{else}}Connect to FormaTeX{{end}}</title>
  <style>
    *{box-sizing:border-box;margin:0;padding:0}
    body{font-family:system-ui,-apple-system,sans-serif;background:#f5f5f5;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:20px;color:#1a1a1a}
    .card{background:#fff;border-radius:12px;padding:36px;max-width:440px;width:100%;box-shadow:0 2px 12px rgba(0,0,0,.08)}
    .logo{font-size:1.1rem;font-weight:700;letter-spacing:-.5px;margin-bottom:24px}
    h1{font-size:1.25rem;font-weight:600;margin-bottom:12px;line-height:1.3}
    p{font-size:.9rem;color:#555;margin-bottom:20px;line-height:1.5}
    strong{color:#1a1a1a;word-break:break-all}
    label{display:block;font-size:.85rem;font-weight:600;margin-bottom:6px;color:#333}
    input[type=text]{width:100%;padding:10px 12px;border:1px solid #767676;border-radius:8px;font-size:.95rem;font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
    .hint{font-size:.8rem;color:#595959;margin-top:8px;margin-bottom:0}
    .error{font-size:.85rem;color:#b00020;margin-top:8px;margin-bottom:0}
    a{color:#1a1a1a}
    .actions{display:flex;gap:10px;margin-top:20px}
    button{flex:1;min-height:44px;padding:10px;border-radius:8px;font-size:.95rem;font-weight:500;cursor:pointer;border:1px solid #1a1a1a}
    .primary{background:#1a1a1a;color:#fff}
    .primary:hover{background:#333}
    .secondary{background:#fff;color:#1a1a1a}
    .secondary:hover{background:#f0f0f0}
    input:focus-visible,button:focus-visible,a:focus-visible{outline:2px solid #1a1a1a;outline-offset:2px}
  </style>
</head>
<body>
  <main class="card">
    <div class="logo">FormaTeX</div>
{{- if .FatalTitle}}
    <h1>{{.FatalTitle}}</h1>
    <p>{{.FatalMessage}}</p>
{{- else}}
    <h1>Connect {{.ClientName}} to FormaTeX</h1>
    <p>{{.ClientName}} wants to compile LaTeX documents with your FormaTeX account. After you authorize, you are sent to <strong>{{.RedirectDisplay}}</strong> and it can use the key you enter until you revoke it. Only continue if you recognize it.</p>
    <form method="post" action="/authorize">
      <input type="hidden" name="response_type" value="code">
      <input type="hidden" name="code_challenge_method" value="S256">
      <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
      <input type="hidden" name="client_id" value="{{.ClientID}}">
      <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
      <input type="hidden" name="state" value="{{.State}}">
{{- if .Resource}}
      <input type="hidden" name="resource" value="{{.Resource}}">
{{- end}}
      <label for="api_key">FormaTeX API key</label>
      <input type="text" id="api_key" name="api_key" placeholder="fex_..." autocomplete="off" autocapitalize="off" spellcheck="false" required autofocus aria-describedby="{{.DescribedBy}}">
{{- if .FormError}}
      <p class="error" id="api_key_error" role="alert">{{.FormError}}</p>
{{- end}}
      <p class="hint" id="api_key_hint">Create or copy a key at <a href="{{.APIKeysURL}}" target="_blank" rel="noopener noreferrer">formatex.io/dashboard/api-keys</a>. Keys start with fex_.</p>
      <div class="actions">
        <button type="submit" class="primary">Authorize</button>
        <button type="submit" class="secondary" name="decision" value="deny" formnovalidate>Cancel</button>
      </div>
    </form>
{{- end}}
  </main>
</body>
</html>
`))

// renderPage writes an HTML page with headers suited to a page that receives
// secrets: not cacheable, not frameable, no referrer, no scripts.
func renderPage(w http.ResponseWriter, status int, data authorizePage) {
	var buf bytes.Buffer
	if err := authorizeTmpl.Execute(&buf, data); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func renderFatal(w http.ResponseWriter, message string) {
	renderPage(w, http.StatusBadRequest, authorizePage{
		FatalTitle:   "Cannot connect",
		FatalMessage: message,
	})
}

func (s *oauthServer) consentPage(req *authRequest, resource, formError string) authorizePage {
	name := req.client.name
	if name == "" {
		name = defaultClientName
	}
	describedBy := "api_key_hint"
	if formError != "" {
		describedBy = "api_key_error api_key_hint"
	}
	return authorizePage{
		ClientName:      name,
		RedirectDisplay: redirectDisplay(req.redirectURL),
		ClientID:        req.clientID,
		RedirectURI:     req.redirectURI,
		State:           req.state,
		CodeChallenge:   req.codeChallenge,
		Resource:        resource,
		FormError:       formError,
		DescribedBy:     describedBy,
		APIKeysURL:      apiKeysURL,
	}
}

// authorizeHandler shows the API-key consent page (GET) and issues an
// authorization code (POST). Both verbs validate the full request: the POST
// body is user-controlled, so hidden form fields are never trusted.
func (s *oauthServer) authorizeHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.authorizeGET(w, r)
	case http.MethodPost:
		s.authorizePOST(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *oauthServer) authorizeGET(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req, fail := s.validateAuthorize(q)
	if fail != nil {
		s.refuse(w, r, req, fail)
		return
	}
	renderPage(w, http.StatusOK, s.consentPage(req, q.Get("resource"), ""))
}

func (s *oauthServer) authorizePOST(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		renderFatal(w, "The request could not be read.")
		return
	}
	form := r.PostForm
	req, fail := s.validateAuthorize(form)
	if fail != nil {
		s.refuse(w, r, req, fail)
		return
	}

	if form.Get("decision") == "deny" {
		s.redirectResult(w, r, req, map[string]string{
			"error":             "access_denied",
			"error_description": "The user denied the request.",
		})
		return
	}

	apiKey := strings.TrimSpace(form.Get("api_key"))
	var formError string
	switch {
	case apiKey == "":
		formError = "Enter your FormaTeX API key."
	case len(apiKey) > maxAPIKeyLen || strings.IndexFunc(apiKey, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		formError = "That does not look like an API key. Copy it again from your dashboard."
	}
	if formError != "" {
		renderPage(w, http.StatusBadRequest, s.consentPage(req, form.Get("resource"), formError))
		return
	}

	code, err := s.addCode(pendingCode{
		clientID:      req.clientID,
		apiKey:        apiKey,
		codeChallenge: req.codeChallenge,
		redirectURI:   req.redirectURI,
	})
	if err != nil {
		renderFatal(w, "Something went wrong on our side. Please try again.")
		return
	}
	s.redirectResult(w, r, req, map[string]string{"code": code})
}

// refuse reports an authorization failure: as an error page when the client or
// redirect URI cannot be trusted, otherwise to the validated redirect URI.
func (s *oauthServer) refuse(w http.ResponseWriter, r *http.Request, req *authRequest, fail *authFailure) {
	if fail.fatal || req == nil {
		renderFatal(w, fail.description)
		return
	}
	s.redirectResult(w, r, req, map[string]string{
		"error":             fail.code,
		"error_description": fail.description,
	})
}

// --- token -----------------------------------------------------------------

// tokenHandler exchanges an authorization code for an access token.
//
// Known limitation: the access token is the user's FormaTeX API key itself,
// not a token minted for this resource. It is therefore not audience-bound,
// carries no scope and does not expire; it stays valid until the user revokes
// the key in the dashboard. The RFC 8707 resource parameter is validated but
// cannot be reflected in the token. expires_in is deliberately omitted:
// clients discard a token when expires_in elapses and force a new
// authorization, which would be wrong for a key that does not expire.
func (s *oauthServer) tokenHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")

	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "request body must be application/x-www-form-urlencoded")
		return
	}
	form := r.PostForm

	switch grant := form.Get("grant_type"); {
	case grant == "":
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "grant_type is required")
		return
	case grant != "authorization_code":
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code is supported")
		return
	}

	code, verifier, clientID := form.Get("code"), form.Get("code_verifier"), form.Get("client_id")
	switch {
	case code == "":
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code is required")
		return
	case verifier == "":
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code_verifier is required")
		return
	case clientID == "":
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "client_id is required")
		return
	}
	for _, res := range form["resource"] {
		if !s.resourceAllowed(res) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource must be "+s.resourceURL())
			return
		}
	}

	if _, ok := s.lookupClient(clientID); !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "unknown, invalid or expired client_id; register again")
		return
	}

	pending, ok := s.consumeCode(code)
	switch {
	case !ok:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid, expired or already used")
		return
	case pending.clientID != clientID:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code was issued to a different client")
		return
	case form.Get("redirect_uri") != "" && form.Get("redirect_uri") != pending.redirectURI:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	case !verifyPKCE(verifier, pending.codeChallenge):
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"access_token": pending.apiKey,
		"token_type":   "Bearer",
	})
}
