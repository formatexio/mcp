package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testBase     = "https://mcp.example.test"
	testRedirect = "https://claude.example/api/mcp/auth_callback"
	testAPIKey   = "fex_test_0123456789abcdef"

	// Signing secrets for client ids (at least minSecretLen bytes).
	testSecret  = "test-secret-0123456789abcdef-0123"
	otherSecret = "other-secret-0123456789abcdef-012"

	// RFC 7636 appendix B test vector.
	testVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	testChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

// --- helpers ---------------------------------------------------------------

// newTestOAuth returns an OAuth server mounted on its own mux, without the MCP
// endpoint, so tests can inspect the store and control the clock. Its rate
// limit is effectively off; TestRateLimiting uses the real one.
func newTestOAuth(t *testing.T) (*oauthServer, http.Handler) {
	t.Helper()
	o := newOAuthServer(testBase, []byte(testSecret))
	o.limiter = newRateLimiter(1<<30, rateLimitWindow)
	mux := http.NewServeMux()
	o.registerRoutes(mux)
	return o, mux
}

func send(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	return send(h, httptest.NewRequest(http.MethodGet, target, nil))
}

func postForm(h http.Handler, path string, v url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return send(h, req)
}

func postJSON(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return send(h, req)
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not a JSON object: %v\nbody: %s", err, rec.Body.String())
	}
	return m
}

func registerClient(t *testing.T, h http.Handler, name string, redirectURIs ...string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"client_name": name, "redirect_uris": redirectURIs})
	if err != nil {
		t.Fatal(err)
	}
	rec := postJSON(h, "/register", string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status %d, body %s", rec.Code, rec.Body.String())
	}
	id, _ := decodeJSON(t, rec)["client_id"].(string)
	if id == "" {
		t.Fatal("register: empty client_id")
	}
	return id
}

func authorizeParams(clientID, redirectURI string) url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {"st-123"},
		"code_challenge":        {testChallenge},
		"code_challenge_method": {"S256"},
	}
}

func location(t *testing.T, rec *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body: %s", rec.Code, rec.Body.String())
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("bad Location: %v", err)
	}
	return u
}

func assertNoRedirect(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("must not redirect, got Location %q", loc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want an HTML error page", ct)
	}
}

// issueCode runs register -> authorize POST and returns the client id and code.
func issueCode(t *testing.T, h http.Handler) (clientID, code string) {
	t.Helper()
	clientID = registerClient(t, h, "Claude", testRedirect)
	form := authorizeParams(clientID, testRedirect)
	form.Set("api_key", testAPIKey)
	loc := location(t, postForm(h, "/authorize", form))
	code = loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in %s", loc)
	}
	return clientID, code
}

func tokenForm(clientID, code string) url.Values {
	return url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {testVerifier},
		"client_id":     {clientID},
		"redirect_uri":  {testRedirect},
	}
}

// --- metadata and MCP challenge ---------------------------------------------

func TestPKCEVector(t *testing.T) {
	h := sha256.Sum256([]byte(testVerifier))
	if got := base64.RawURLEncoding.EncodeToString(h[:]); got != testChallenge {
		t.Fatalf("test vector is wrong: %s", got)
	}
	if !verifyPKCE(testVerifier, testChallenge) {
		t.Error("verifyPKCE rejected a valid pair")
	}
	if verifyPKCE("x"+testVerifier, testChallenge) {
		t.Error("verifyPKCE accepted a wrong verifier")
	}
}

func TestProtectedResourceMetadata(t *testing.T) {
	h := newHTTPMux("http://127.0.0.1:1/", testBase, []byte(testSecret))

	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	} {
		t.Run(path, func(t *testing.T) {
			rec := get(h, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Errorf("Access-Control-Allow-Origin = %q", got)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q", ct)
			}
			doc := decodeJSON(t, rec)
			if doc["resource"] != testBase+"/mcp" {
				t.Errorf("resource = %v", doc["resource"])
			}
			servers, _ := doc["authorization_servers"].([]any)
			if len(servers) != 1 || servers[0] != testBase {
				t.Errorf("authorization_servers = %v", doc["authorization_servers"])
			}
			methods, _ := doc["bearer_methods_supported"].([]any)
			if len(methods) != 1 || methods[0] != "header" {
				t.Errorf("bearer_methods_supported = %v", doc["bearer_methods_supported"])
			}
			if doc["resource_name"] != "FormaTeX MCP" {
				t.Errorf("resource_name = %v", doc["resource_name"])
			}

			pre := send(h, httptest.NewRequest(http.MethodOptions, path, nil))
			if pre.Code != http.StatusNoContent {
				t.Errorf("OPTIONS status = %d, want 204", pre.Code)
			}
			if pre.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Error("OPTIONS response lacks CORS headers")
			}
			if rec := send(h, httptest.NewRequest(http.MethodPost, path, nil)); rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("POST status = %d, want 405", rec.Code)
			}
		})
	}
}

func TestOAuthRoutesAbsentWithoutBaseURL(t *testing.T) {
	h := newHTTPMux("http://127.0.0.1:1/", "", []byte(testSecret))
	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server",
		"/authorize",
		"/token",
		"/register",
	} {
		if rec := get(h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestAuthorizationServerMetadata(t *testing.T) {
	// A trailing slash in the configured base URL must not leak into the
	// issuer or the endpoint URLs.
	h := newHTTPMux("http://127.0.0.1:1/", testBase+"/", []byte(testSecret))
	doc := decodeJSON(t, get(h, "/.well-known/oauth-authorization-server"))
	if doc["issuer"] != testBase {
		t.Errorf("issuer = %v", doc["issuer"])
	}
	if doc["authorization_endpoint"] != testBase+"/authorize" {
		t.Errorf("authorization_endpoint = %v", doc["authorization_endpoint"])
	}
	if doc["authorization_response_iss_parameter_supported"] != true {
		t.Errorf("authorization_response_iss_parameter_supported = %v", doc["authorization_response_iss_parameter_supported"])
	}
	if got := decodeJSON(t, get(h, "/.well-known/oauth-protected-resource"))["resource"]; got != testBase+"/mcp" {
		t.Errorf("resource = %v", got)
	}
}

func TestMCPUnauthorizedChallenge(t *testing.T) {
	t.Run("with base URL", func(t *testing.T) {
		h := newHTTPMux("http://127.0.0.1:1/", testBase, []byte(testSecret))
		rec := postJSON(h, "/mcp", `{}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		want := `Bearer realm="formatex-mcp", resource_metadata="` + testBase + `/.well-known/oauth-protected-resource"`
		if got := rec.Header().Get("WWW-Authenticate"); got != want {
			t.Errorf("WWW-Authenticate = %q, want %q", got, want)
		}
		if decodeJSON(t, rec)["error"] == nil {
			t.Error("401 body lost its JSON error")
		}
	})

	t.Run("without base URL", func(t *testing.T) {
		h := newHTTPMux("http://127.0.0.1:1/", "", []byte(testSecret))
		rec := postJSON(h, "/mcp", `{}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="formatex-mcp"` {
			t.Errorf("WWW-Authenticate = %q", got)
		}
	})

	t.Run("key present", func(t *testing.T) {
		h := newHTTPMux("http://127.0.0.1:1/", testBase, []byte(testSecret))
		for _, set := range []func(*http.Request){
			func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testAPIKey) },
			func(r *http.Request) { r.Header.Set("X-API-Key", testAPIKey) },
		} {
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			set(req)
			rec := send(h, req)
			if rec.Code == http.StatusUnauthorized {
				t.Errorf("request with a key got 401")
			}
			if rec.Header().Get("WWW-Authenticate") != "" {
				t.Error("unexpected challenge on an authenticated request")
			}
		}
	})
}

// --- registration -------------------------------------------------------------

func TestRegisterValidation(t *testing.T) {
	_, h := newTestOAuth(t)

	tests := []struct {
		name      string
		body      string
		wantCode  int
		wantError string
		wantURIs  []string
	}{
		{"https", `{"redirect_uris":["https://claude.example/cb"]}`, 201, "", []string{"https://claude.example/cb"}},
		{"loopback ipv4 with port", `{"redirect_uris":["http://127.0.0.1:53682/callback"]}`, 201, "", []string{"http://127.0.0.1:53682/callback"}},
		{"loopback localhost with port", `{"redirect_uris":["http://localhost:8080/cb"]}`, 201, "", []string{"http://localhost:8080/cb"}},
		{"loopback ipv6 with port", `{"redirect_uris":["http://[::1]:9000/cb"]}`, 201, "", []string{"http://[::1]:9000/cb"}},
		{"custom scheme with host", `{"redirect_uris":["cursor://anysphere.cursor-retrieval/oauth/callback"]}`, 201, "", []string{"cursor://anysphere.cursor-retrieval/oauth/callback"}},
		{"custom scheme with path only", `{"redirect_uris":["com.example.app:/oauth2redirect"]}`, 201, "", []string{"com.example.app:/oauth2redirect"}},
		{"valid and invalid keeps the valid one", `{"redirect_uris":["https://claude.example/cb","javascript:alert(1)"]}`, 201, "", []string{"https://claude.example/cb"}},
		{"duplicates collapse", `{"redirect_uris":["https://claude.example/cb","https://claude.example/cb"]}`, 201, "", []string{"https://claude.example/cb"}},

		{"http on a public host", `{"redirect_uris":["http://example.com/cb"]}`, 400, "invalid_redirect_uri", nil},
		{"loopback lookalike", `{"redirect_uris":["http://localhost.evil.example/cb"]}`, 400, "invalid_redirect_uri", nil},
		{"javascript", `{"redirect_uris":["javascript:alert(1)"]}`, 400, "invalid_redirect_uri", nil},
		{"data", `{"redirect_uris":["data:text/html,hi"]}`, 400, "invalid_redirect_uri", nil},
		{"file", `{"redirect_uris":["file:///etc/passwd"]}`, 400, "invalid_redirect_uri", nil},
		{"vbscript", `{"redirect_uris":["vbscript:msgbox(1)"]}`, 400, "invalid_redirect_uri", nil},
		{"about", `{"redirect_uris":["about:blank"]}`, 400, "invalid_redirect_uri", nil},
		{"blob", `{"redirect_uris":["blob:https://example.com/id"]}`, 400, "invalid_redirect_uri", nil},
		{"fragment", `{"redirect_uris":["https://example.com/cb#frag"]}`, 400, "invalid_redirect_uri", nil},
		{"empty fragment", `{"redirect_uris":["https://example.com/cb#"]}`, 400, "invalid_redirect_uri", nil},
		{"userinfo", `{"redirect_uris":["https://user:pw@example.com/cb"]}`, 400, "invalid_redirect_uri", nil},
		{"userinfo without password", `{"redirect_uris":["https://example.com@evil.example/cb"]}`, 400, "invalid_redirect_uri", nil},
		{"relative", `{"redirect_uris":["/cb"]}`, 400, "invalid_redirect_uri", nil},
		{"scheme relative", `{"redirect_uris":["//evil.example/cb"]}`, 400, "invalid_redirect_uri", nil},
		{"https without host", `{"redirect_uris":["https:///cb"]}`, 400, "invalid_redirect_uri", nil},
		{"backslash", `{"redirect_uris":["https://good.example\\@evil.example/cb"]}`, 400, "invalid_redirect_uri", nil},
		{"empty string", `{"redirect_uris":[""]}`, 400, "invalid_redirect_uri", nil},
		{"empty list", `{"redirect_uris":[]}`, 400, "invalid_redirect_uri", nil},
		{"missing list", `{"client_name":"x"}`, 400, "invalid_redirect_uri", nil},
		{"not json", `not json`, 400, "invalid_client_metadata", nil},
		{"grant types without authorization_code", `{"redirect_uris":["https://claude.example/cb"],"grant_types":["client_credentials"]}`, 400, "invalid_client_metadata", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := postJSON(h, "/register", tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			doc := decodeJSON(t, rec)
			if tc.wantError != "" {
				if doc["error"] != tc.wantError {
					t.Errorf("error = %v, want %s", doc["error"], tc.wantError)
				}
				return
			}
			if id, _ := doc["client_id"].(string); id == "" || id == "formatex-mcp-client" {
				t.Errorf("client_id = %q", id)
			}
			if doc["token_endpoint_auth_method"] != "none" {
				t.Errorf("token_endpoint_auth_method = %v", doc["token_endpoint_auth_method"])
			}
			var got []string
			for _, u := range doc["redirect_uris"].([]any) {
				got = append(got, u.(string))
			}
			if strings.Join(got, ",") != strings.Join(tc.wantURIs, ",") {
				t.Errorf("redirect_uris = %v, want %v", got, tc.wantURIs)
			}
		})
	}
}

func TestRegisterEchoesMetadata(t *testing.T) {
	_, h := newTestOAuth(t)
	body := `{"client_name":"  Claude\u202e  Desktop ","redirect_uris":["https://claude.example/cb"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"client_secret_post"}`

	a := decodeJSON(t, postJSON(h, "/register", body))
	if a["client_name"] != "Claude Desktop" {
		t.Errorf("client_name = %q, want the sanitized name", a["client_name"])
	}
	if gt, _ := a["grant_types"].([]any); len(gt) != 1 || gt[0] != "authorization_code" {
		t.Errorf("grant_types = %v", a["grant_types"])
	}
	if rt, _ := a["response_types"].([]any); len(rt) != 1 || rt[0] != "code" {
		t.Errorf("response_types = %v", a["response_types"])
	}
	if a["token_endpoint_auth_method"] != "none" {
		t.Errorf("token_endpoint_auth_method = %v", a["token_endpoint_auth_method"])
	}
	if _, present := a["client_secret"]; present {
		t.Error("public client must not receive a secret")
	}
}

// --- authorize ----------------------------------------------------------------

func TestAuthorizeRejectsUnknownClient(t *testing.T) {
	o, h := newTestOAuth(t)
	params := authorizeParams("formatex-mcp-client", "https://evil.example/steal")

	rec := get(h, "/authorize?"+params.Encode())
	assertNoRedirect(t, rec)
	if strings.Contains(rec.Body.String(), "evil.example") {
		t.Error("error page must not echo the unvalidated redirect URI")
	}

	params.Set("api_key", testAPIKey)
	assertNoRedirect(t, postForm(h, "/authorize", params))
	if len(o.codes) != 0 {
		t.Errorf("%d codes issued for an unknown client", len(o.codes))
	}
}

func TestAuthorizeRejectsUnregisteredRedirect(t *testing.T) {
	o, h := newTestOAuth(t)
	id := registerClient(t, h, "Claude", "https://claude.example/cb", "http://127.0.0.1:7777/cb")

	for _, redirect := range []string{
		"https://evil.example/cb",
		"https://claude.example/cb/",
		"https://claude.example/cb?x=1",
		"https://claude.example.evil.example/cb",
		"http://claude.example/cb",
		"http://127.0.0.1:7778/cb",
		"javascript:alert(1)",
	} {
		t.Run(redirect, func(t *testing.T) {
			params := authorizeParams(id, redirect)
			assertNoRedirect(t, get(h, "/authorize?"+params.Encode()))

			params.Set("api_key", testAPIKey)
			assertNoRedirect(t, postForm(h, "/authorize", params))
		})
	}
	if len(o.codes) != 0 {
		t.Errorf("%d codes issued for mismatched redirect URIs", len(o.codes))
	}

	t.Run("missing with several registered", func(t *testing.T) {
		params := authorizeParams(id, "")
		params.Del("redirect_uri")
		assertNoRedirect(t, get(h, "/authorize?"+params.Encode()))
	})

	t.Run("repeated parameter", func(t *testing.T) {
		q := authorizeParams(id, "https://claude.example/cb").Encode() + "&redirect_uri=" + url.QueryEscape("https://evil.example/cb")
		assertNoRedirect(t, get(h, "/authorize?"+q))
	})
}

func TestAuthorizeSingleRegisteredRedirectMayBeOmitted(t *testing.T) {
	_, h := newTestOAuth(t)
	id := registerClient(t, h, "Claude", testRedirect)
	params := authorizeParams(id, testRedirect)
	params.Del("redirect_uri")
	if rec := get(h, "/authorize?"+params.Encode()); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAuthorizeProtocolErrorsRedirectToRegisteredURI(t *testing.T) {
	_, h := newTestOAuth(t)
	id := registerClient(t, h, "Claude", testRedirect)

	tests := []struct {
		name    string
		mutate  func(url.Values)
		wantErr string
	}{
		{"missing challenge", func(v url.Values) { v.Del("code_challenge") }, "invalid_request"},
		{"empty challenge", func(v url.Values) { v.Set("code_challenge", "") }, "invalid_request"},
		{"malformed challenge", func(v url.Values) { v.Set("code_challenge", "short") }, "invalid_request"},
		{"plain method", func(v url.Values) { v.Set("code_challenge_method", "plain") }, "invalid_request"},
		{"missing method", func(v url.Values) { v.Del("code_challenge_method") }, "invalid_request"},
		{"token response type", func(v url.Values) { v.Set("response_type", "token") }, "unsupported_response_type"},
		{"missing response type", func(v url.Values) { v.Del("response_type") }, "invalid_request"},
		{"foreign resource", func(v url.Values) { v.Set("resource", "https://other.example/mcp") }, "invalid_target"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := authorizeParams(id, testRedirect)
			tc.mutate(params)
			loc := location(t, get(h, "/authorize?"+params.Encode()))

			if loc.Scheme+"://"+loc.Host+loc.Path != testRedirect {
				t.Errorf("redirected to %s, want the registered URI", loc)
			}
			q := loc.Query()
			if q.Get("error") != tc.wantErr {
				t.Errorf("error = %q, want %q", q.Get("error"), tc.wantErr)
			}
			if q.Get("state") != "st-123" {
				t.Errorf("state = %q", q.Get("state"))
			}
			if q.Get("iss") != testBase {
				t.Errorf("iss = %q", q.Get("iss"))
			}
			if q.Get("code") != "" {
				t.Error("an error response must not carry a code")
			}
		})
	}

	for _, resource := range []string{testBase + "/mcp", testBase, testBase + "/"} {
		params := authorizeParams(id, testRedirect)
		params.Set("resource", resource)
		if rec := get(h, "/authorize?"+params.Encode()); rec.Code != http.StatusOK {
			t.Errorf("resource %q: status = %d, want 200", resource, rec.Code)
		}
	}
}

func TestAuthorizePage(t *testing.T) {
	_, h := newTestOAuth(t)

	t.Run("shows client and destination", func(t *testing.T) {
		id := registerClient(t, h, "Acme <b>Assistant</b>", "https://assistant.acme.example/callback")
		rec := get(h, "/authorize?"+authorizeParams(id, "https://assistant.acme.example/callback").Encode())
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			"Acme &lt;b&gt;Assistant&lt;/b&gt;",
			"assistant.acme.example",
			"fex_...",
			"https://formatex.io/dashboard/api-keys",
			`spellcheck="false"`,
			`autocomplete="off"`,
			`<label for="api_key">`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("page lacks %q", want)
			}
		}
		for _, bad := range []string{"<b>Assistant</b>", "fmtx_", "Settings", "Connect to Claude", `type="password"`} {
			if strings.Contains(body, bad) {
				t.Errorf("page contains %q", bad)
			}
		}
		for header, want := range map[string]string{
			"X-Frame-Options": "DENY",
			"Cache-Control":   "no-store",
			"Referrer-Policy": "no-referrer",
		} {
			if got := rec.Header().Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("Content-Security-Policy = %q", csp)
		}
	})

	t.Run("anonymous client falls back", func(t *testing.T) {
		id := registerClient(t, h, "", testRedirect)
		rec := get(h, "/authorize?"+authorizeParams(id, testRedirect).Encode())
		if !strings.Contains(rec.Body.String(), "Connect an AI client to FormaTeX") {
			t.Errorf("fallback heading missing:\n%s", rec.Body.String())
		}
	})

	t.Run("loopback app is labelled", func(t *testing.T) {
		id := registerClient(t, h, "CLI", "http://127.0.0.1:4242/cb")
		rec := get(h, "/authorize?"+authorizeParams(id, "http://127.0.0.1:4242/cb").Encode())
		if !strings.Contains(rec.Body.String(), "127.0.0.1:4242 (an app on this computer)") {
			t.Error("loopback destination not described")
		}
	})
}

func TestAuthorizeFormErrorsAndDeny(t *testing.T) {
	o, h := newTestOAuth(t)
	id := registerClient(t, h, "Claude", testRedirect)

	t.Run("empty key re-renders the form", func(t *testing.T) {
		form := authorizeParams(id, testRedirect)
		form.Set("api_key", "   ")
		rec := postForm(h, "/authorize", form)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Fatalf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
		}
		if !strings.Contains(rec.Body.String(), "Enter your FormaTeX API key.") {
			t.Error("inline error missing")
		}
		if !strings.Contains(rec.Body.String(), `role="alert"`) {
			t.Error("error is not announced")
		}
	})

	t.Run("key with inner whitespace", func(t *testing.T) {
		form := authorizeParams(id, testRedirect)
		form.Set("api_key", "fex_abc def")
		if rec := postForm(h, "/authorize", form); rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("deny", func(t *testing.T) {
		form := authorizeParams(id, testRedirect)
		form.Set("decision", "deny")
		loc := location(t, postForm(h, "/authorize", form))
		if loc.Query().Get("error") != "access_denied" || loc.Query().Get("code") != "" {
			t.Errorf("Location = %s", loc)
		}
	})

	if len(o.codes) != 0 {
		t.Errorf("%d codes issued", len(o.codes))
	}
}

// --- full flow and token endpoint ----------------------------------------------

func TestOAuthHappyPath(t *testing.T) {
	_, h := newTestOAuth(t)
	clientID := registerClient(t, h, "Claude", testRedirect)

	if rec := get(h, "/authorize?"+authorizeParams(clientID, testRedirect).Encode()); rec.Code != http.StatusOK {
		t.Fatalf("authorize GET: status %d", rec.Code)
	}

	form := authorizeParams(clientID, testRedirect)
	form.Set("api_key", "  "+testAPIKey+"\n")
	loc := location(t, postForm(h, "/authorize", form))
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != testRedirect {
		t.Fatalf("redirected to %s", got)
	}
	q := loc.Query()
	if q.Get("state") != "st-123" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("iss") != testBase {
		t.Errorf("iss = %q", q.Get("iss"))
	}
	code := q.Get("code")
	if code == "" {
		t.Fatal("no code")
	}

	rec := postForm(h, "/token", tokenForm(clientID, code))
	if rec.Code != http.StatusOK {
		t.Fatalf("token: status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	doc := decodeJSON(t, rec)
	if doc["access_token"] != testAPIKey {
		t.Errorf("access_token = %v", doc["access_token"])
	}
	if doc["token_type"] != "Bearer" {
		t.Errorf("token_type = %v", doc["token_type"])
	}
	if _, present := doc["expires_in"]; present {
		t.Error("expires_in must be omitted: the API key does not expire")
	}
}

func TestTokenRejections(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(url.Values)
		wantError string
	}{
		{"wrong verifier", func(v url.Values) { v.Set("code_verifier", "wrong-"+testVerifier) }, "invalid_grant"},
		{"wrong redirect_uri", func(v url.Values) { v.Set("redirect_uri", "https://evil.example/cb") }, "invalid_grant"},
		{"unknown client", func(v url.Values) { v.Set("client_id", "mcp_unknown") }, "invalid_client"},
		{"unknown code", func(v url.Values) { v.Set("code", "nope") }, "invalid_grant"},
		{"wrong grant type", func(v url.Values) { v.Set("grant_type", "refresh_token") }, "unsupported_grant_type"},
		{"missing grant type", func(v url.Values) { v.Del("grant_type") }, "invalid_request"},
		{"missing verifier", func(v url.Values) { v.Del("code_verifier") }, "invalid_request"},
		{"missing client_id", func(v url.Values) { v.Del("client_id") }, "invalid_request"},
		{"foreign resource", func(v url.Values) { v.Set("resource", "https://other.example/mcp") }, "invalid_target"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, h := newTestOAuth(t)
			clientID, code := issueCode(t, h)
			form := tokenForm(clientID, code)
			tc.mutate(form)

			rec := postForm(h, "/token", form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			doc := decodeJSON(t, rec)
			if doc["error"] != tc.wantError {
				t.Errorf("error = %v, want %s", doc["error"], tc.wantError)
			}
			if _, leaked := doc["access_token"]; leaked {
				t.Error("error response leaked a token")
			}
		})
	}

	t.Run("reused code", func(t *testing.T) {
		_, h := newTestOAuth(t)
		clientID, code := issueCode(t, h)
		if rec := postForm(h, "/token", tokenForm(clientID, code)); rec.Code != http.StatusOK {
			t.Fatalf("first exchange: status %d", rec.Code)
		}
		rec := postForm(h, "/token", tokenForm(clientID, code))
		if rec.Code != http.StatusBadRequest || decodeJSON(t, rec)["error"] != "invalid_grant" {
			t.Errorf("second exchange: status %d, body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("failed attempt burns the code", func(t *testing.T) {
		_, h := newTestOAuth(t)
		clientID, code := issueCode(t, h)
		bad := tokenForm(clientID, code)
		bad.Set("code_verifier", "wrong-"+testVerifier)
		if rec := postForm(h, "/token", bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", rec.Code)
		}
		if rec := postForm(h, "/token", tokenForm(clientID, code)); rec.Code != http.StatusBadRequest {
			t.Errorf("code survived a failed exchange: status %d", rec.Code)
		}
	})

	t.Run("code issued to another client", func(t *testing.T) {
		_, h := newTestOAuth(t)
		_, code := issueCode(t, h)
		other := registerClient(t, h, "Other", "https://other.example/cb")
		form := tokenForm(other, code)
		form.Set("redirect_uri", "https://other.example/cb")
		rec := postForm(h, "/token", form)
		if rec.Code != http.StatusBadRequest || decodeJSON(t, rec)["error"] != "invalid_grant" {
			t.Errorf("status %d, body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("redirect_uri may be omitted", func(t *testing.T) {
		_, h := newTestOAuth(t)
		clientID, code := issueCode(t, h)
		form := tokenForm(clientID, code)
		form.Del("redirect_uri")
		if rec := postForm(h, "/token", form); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("method", func(t *testing.T) {
		_, h := newTestOAuth(t)
		if rec := get(h, "/token"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET /token: status = %d, want 405", rec.Code)
		}
	})
}

// --- lifetime and bounds -------------------------------------------------------

func TestCodeAndClientExpiry(t *testing.T) {
	o, h := newTestOAuth(t)
	now := time.Now()
	o.now = func() time.Time { return now }

	clientID, code := issueCode(t, h)

	now = now.Add(codeTTL + time.Second)
	rec := postForm(h, "/token", tokenForm(clientID, code))
	if rec.Code != http.StatusBadRequest || decodeJSON(t, rec)["error"] != "invalid_grant" {
		t.Errorf("expired code: status %d, body %s", rec.Code, rec.Body.String())
	}

	// The client is still registered a day later but not after the TTL.
	now = now.Add(24 * time.Hour)
	if rec := get(h, "/authorize?"+authorizeParams(clientID, testRedirect).Encode()); rec.Code != http.StatusOK {
		t.Errorf("client rejected inside its TTL: status %d", rec.Code)
	}
	now = now.Add(clientTTL)
	assertNoRedirect(t, get(h, "/authorize?"+authorizeParams(clientID, testRedirect).Encode()))
}

func TestCodeStoreIsBounded(t *testing.T) {
	o := newOAuthServer(testBase, []byte(testSecret))
	now := time.Now()
	o.now = func() time.Time { return now }

	for i := 0; i < maxCodes+5; i++ {
		now = now.Add(time.Millisecond)
		if _, err := o.addCode(pendingCode{clientID: "c"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(o.codes) != maxCodes {
		t.Errorf("codes = %d, want %d", len(o.codes), maxCodes)
	}
}

// --- stateless client registrations ---------------------------------------------

// newRestartedOAuth simulates a new process: no shared state, given secret.
func newRestartedOAuth(secret string) http.Handler {
	o := newOAuthServer(testBase, []byte(secret))
	o.limiter = newRateLimiter(1<<30, rateLimitWindow)
	mux := http.NewServeMux()
	o.registerRoutes(mux)
	return mux
}

func TestRegistrationSurvivesRestart(t *testing.T) {
	_, before := newTestOAuth(t)
	clientID := registerClient(t, before, "Claude", testRedirect)

	after := newRestartedOAuth(testSecret)

	rec := get(after, "/authorize?"+authorizeParams(clientID, testRedirect).Encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize after restart: status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Connect Claude to FormaTeX") {
		t.Error("client name was not carried by the client_id")
	}

	form := authorizeParams(clientID, testRedirect)
	form.Set("api_key", testAPIKey)
	code := location(t, postForm(after, "/authorize", form)).Query().Get("code")
	rec = postForm(after, "/token", tokenForm(clientID, code))
	if rec.Code != http.StatusOK || decodeJSON(t, rec)["access_token"] != testAPIKey {
		t.Fatalf("token after restart: status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestRegistrationRejectedWithDifferentSecret(t *testing.T) {
	_, h := newTestOAuth(t)
	clientID := registerClient(t, h, "Claude", testRedirect)

	other := newRestartedOAuth(otherSecret)
	assertNoRedirect(t, get(other, "/authorize?"+authorizeParams(clientID, testRedirect).Encode()))

	rec := postForm(other, "/token", tokenForm(clientID, "any"))
	if rec.Code != http.StatusBadRequest || decodeJSON(t, rec)["error"] != "invalid_client" {
		t.Errorf("token: status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestClientIDTamperingIsRejected(t *testing.T) {
	secret := []byte(testSecret)
	now := time.Now()
	payload := clientPayload{Name: "Claude", RedirectURIs: []string{testRedirect}, Expiry: now.Add(time.Hour).Unix()}
	id, err := encodeClientID(secret, payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decodeClientID(secret, id, now); !ok {
		t.Fatal("a freshly signed id must verify")
	}

	body, sig, _ := strings.Cut(strings.TrimPrefix(id, clientIDPrefix), ".")
	evil, err := encodeClientID(secret, clientPayload{RedirectURIs: []string{"https://evil.example/cb"}, Expiry: payload.Expiry})
	if err != nil {
		t.Fatal(err)
	}
	evilBody, _, _ := strings.Cut(strings.TrimPrefix(evil, clientIDPrefix), ".")
	foreign, err := encodeClientID([]byte(otherSecret), payload)
	if err != nil {
		t.Fatal(err)
	}

	flip := func(s string) string {
		if s[0] == 'A' {
			return "B" + s[1:]
		}
		return "A" + s[1:]
	}

	tests := map[string]string{
		"payload swapped, signature kept": clientIDPrefix + evilBody + "." + sig,
		"payload bit flipped":             clientIDPrefix + flip(body) + "." + sig,
		"signature bit flipped":           clientIDPrefix + body + "." + flip(sig),
		"signature removed":               clientIDPrefix + body + ".",
		"no separator":                    clientIDPrefix + body,
		"wrong prefix":                    "xyz_" + body + "." + sig,
		"no prefix":                       body + "." + sig,
		"signed with another secret":      foreign,
		"not base64":                      clientIDPrefix + "!!!." + "???",
		"empty":                           "",
		"legacy fixed id":                 "formatex-mcp-client",
		"too long":                        clientIDPrefix + strings.Repeat("A", maxClientIDLen) + "." + sig,
	}
	for name, bad := range tests {
		t.Run(name, func(t *testing.T) {
			if _, ok := decodeClientID(secret, bad, now); ok {
				t.Errorf("accepted %q", bad)
			}
		})
	}

	// And through HTTP: the authorize page must be a dead end, never a redirect.
	_, h := newTestOAuth(t)
	tampered := clientIDPrefix + evilBody + "." + sig
	params := authorizeParams(tampered, "https://evil.example/cb")
	assertNoRedirect(t, get(h, "/authorize?"+params.Encode()))
}

func TestClientIDExpiry(t *testing.T) {
	secret := []byte(testSecret)
	now := time.Now()

	mk := func(exp time.Time) string {
		id, err := encodeClientID(secret, clientPayload{RedirectURIs: []string{testRedirect}, Expiry: exp.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	if _, ok := decodeClientID(secret, mk(now.Add(time.Minute)), now); !ok {
		t.Error("an unexpired id was rejected")
	}
	if _, ok := decodeClientID(secret, mk(now.Add(-time.Minute)), now); ok {
		t.Error("an expired id was accepted")
	}
}

func TestClientIDRevalidatesRedirectURIs(t *testing.T) {
	secret := []byte(testSecret)
	now := time.Now()
	exp := now.Add(time.Hour).Unix()

	tests := map[string][]string{
		"javascript":     {"javascript:alert(1)"},
		"http public":    {"http://example.com/cb"},
		"fragment":       {"https://example.com/cb#x"},
		"userinfo":       {"https://u:p@example.com/cb"},
		"valid + bad":    {testRedirect, "data:text/html,x"},
		"empty list":     {},
		"too many":       {"https://a.example/1", "https://a.example/2", "https://a.example/3", "https://a.example/4", "https://a.example/5", "https://a.example/6"},
		"non-ascii host": {"https://ex\u0430mple.com/cb"},
	}
	for name, uris := range tests {
		t.Run(name, func(t *testing.T) {
			// Correctly signed, so only the defense-in-depth check can stop it.
			id, err := encodeClientID(secret, clientPayload{RedirectURIs: uris, Expiry: exp})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := decodeClientID(secret, id, now); ok {
				t.Errorf("accepted a signed id with redirect URIs %v", uris)
			}
		})
	}
}

func TestRegisterBoundsClientIDSize(t *testing.T) {
	_, h := newTestOAuth(t)

	t.Run("largest typical registration fits", func(t *testing.T) {
		uri := "https://app.example/" + strings.Repeat("p", 100)
		name := strings.Repeat("n", maxClientNameRunes)
		body, _ := json.Marshal(map[string]any{"client_name": name, "redirect_uris": []string{uri, uri + "2", uri + "3"}})
		rec := postJSON(h, "/register", string(body))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
		}
		if id := decodeJSON(t, rec)["client_id"].(string); len(id) > maxClientIDLen {
			t.Errorf("client_id is %d bytes, cap is %d", len(id), maxClientIDLen)
		}
	})

	t.Run("oversized metadata is refused", func(t *testing.T) {
		var uris []string
		for i := 0; i < maxRedirectURIs; i++ {
			uris = append(uris, "https://app.example/"+strings.Repeat(string(rune('a'+i)), maxRedirectURILen-40))
		}
		body, _ := json.Marshal(map[string]any{"redirect_uris": uris})
		rec := postJSON(h, "/register", string(body))
		if rec.Code != http.StatusBadRequest || decodeJSON(t, rec)["error"] != "invalid_client_metadata" {
			t.Errorf("status %d, body %s", rec.Code, rec.Body.String())
		}
	})
}

func TestLoadOAuthSecret(t *testing.T) {
	t.Run("configured", func(t *testing.T) {
		secret, persistent, warning := loadOAuthSecret(testSecret + "\n")
		if !persistent || warning != "" || string(secret) != testSecret {
			t.Errorf("secret %q, persistent %v, warning %q", secret, persistent, warning)
		}
	})

	t.Run("unset falls back to a random key", func(t *testing.T) {
		a, persistent, warning := loadOAuthSecret("")
		b, _, _ := loadOAuthSecret("")
		if persistent || len(a) < minSecretLen {
			t.Errorf("persistent %v, len %d", persistent, len(a))
		}
		if !strings.Contains(warning, "MCP_OAUTH_SECRET is not set") || !strings.Contains(warning, "restart") {
			t.Errorf("warning = %q", warning)
		}
		if string(a) == string(b) {
			t.Error("fallback key is not random")
		}
	})

	t.Run("too short is ignored", func(t *testing.T) {
		secret, persistent, warning := loadOAuthSecret("short")
		if persistent || string(secret) == "short" || len(secret) < minSecretLen {
			t.Errorf("secret %q, persistent %v", secret, persistent)
		}
		if !strings.Contains(warning, "shorter than 32 bytes") {
			t.Errorf("warning = %q", warning)
		}
	})
}

// --- rate limiting ---------------------------------------------------------------

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		cf, xff    string
		remoteAddr string
		want       string
	}{
		{"cloudflare header wins", "203.0.113.7", "198.51.100.1", "10.0.0.1:5000", "203.0.113.7"},
		{"cloudflare header is not an ip", "garbage", "198.51.100.1, 10.0.0.2", "10.0.0.1:5000", "198.51.100.1"},
		{"first forwarded entry", "", "198.51.100.1, 10.0.0.2", "10.0.0.1:5000", "198.51.100.1"},
		{"forwarded entry is not an ip", "", "unknown", "10.0.0.1:5000", "10.0.0.1"},
		{"socket address", "", "", "192.0.2.5:1234", "192.0.2.5"},
		{"ipv6 socket address", "", "", "[2001:db8::1]:443", "2001:db8::1"},
		{"ipv6 header", "2001:DB8::1", "", "10.0.0.1:5000", "2001:db8::1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/token", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.cf != "" {
				req.Header.Set("CF-Connecting-IP", tc.cf)
			}
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientIP(req); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRateLimiting(t *testing.T) {
	o := newOAuthServer(testBase, []byte(testSecret))
	now := time.Now()
	o.now = func() time.Time { return now }
	mux := http.NewServeMux()
	o.registerRoutes(mux)

	post := func(path, ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("CF-Connecting-IP", ip)
		return send(mux, req)
	}
	const ip, otherIP = "198.51.100.1", "198.51.100.2"

	for i := 0; i < rateLimitRequests; i++ {
		if rec := post("/register", ip); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d was limited, the limit is %d", i+1, rateLimitRequests)
		}
	}

	t.Run("request over the limit", func(t *testing.T) {
		rec := post("/register", ip)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", rec.Code)
		}
		if got := rec.Header().Get("Retry-After"); got != "60" {
			t.Errorf("Retry-After = %q, want 60", got)
		}
		if got := decodeJSON(t, rec)["error"]; got != "temporarily_unavailable" {
			t.Errorf("error = %v", got)
		}
	})

	t.Run("other client unaffected", func(t *testing.T) {
		if rec := post("/register", otherIP); rec.Code == http.StatusTooManyRequests {
			t.Error("a different IP was limited")
		}
	})

	t.Run("budget is per endpoint", func(t *testing.T) {
		if rec := post("/token", ip); rec.Code == http.StatusTooManyRequests {
			t.Error("exhausting /register limited /token")
		}
	})

	t.Run("authorize refuses with an HTML page", func(t *testing.T) {
		for i := 0; i < rateLimitRequests; i++ {
			post("/authorize", ip)
		}
		rec := post("/authorize", ip)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q", ct)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Error("Retry-After missing")
		}
	})

	t.Run("only POST is limited", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
		req.Header.Set("CF-Connecting-IP", ip)
		if rec := send(mux, req); rec.Code != http.StatusOK {
			t.Errorf("metadata: status %d", rec.Code)
		}
		req = httptest.NewRequest(http.MethodGet, "/authorize", nil)
		req.Header.Set("CF-Connecting-IP", ip)
		if rec := send(mux, req); rec.Code == http.StatusTooManyRequests {
			t.Error("GET /authorize was limited")
		}
	})

	t.Run("window resets", func(t *testing.T) {
		now = now.Add(45 * time.Second)
		rec := post("/register", ip)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "15" {
			t.Errorf("mid-window: status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
		}
		now = now.Add(16 * time.Second)
		if rec := post("/register", ip); rec.Code == http.StatusTooManyRequests {
			t.Error("still limited after the window passed")
		}
	})
}

func TestRateLimiterIsBounded(t *testing.T) {
	l := newRateLimiter(rateLimitRequests, rateLimitWindow)
	now := time.Now()
	for i := 0; i < maxLimiterKeys+5; i++ {
		now = now.Add(time.Millisecond)
		l.allow(strconv.Itoa(i), now)
	}
	if len(l.windows) > maxLimiterKeys {
		t.Errorf("windows = %d, want at most %d", len(l.windows), maxLimiterKeys)
	}

	// Expired windows are swept.
	now = now.Add(2 * rateLimitWindow)
	l.allow("fresh", now)
	if len(l.windows) != 1 {
		t.Errorf("windows after sweep = %d, want 1", len(l.windows))
	}
}
