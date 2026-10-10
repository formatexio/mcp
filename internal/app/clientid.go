package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Dynamic client registrations are stateless. The client_id is a signed token
//
//	mcp_<base64url(JSON payload)>.<base64url(HMAC-SHA256(secret, base64url(payload)))>
//
// that carries the client name, its validated redirect URIs and an expiry. The
// server keeps no per-client state, so registrations survive restarts and
// redeploys as long as MCP_OAUTH_SECRET is stable, and a client cannot forge an
// id for a redirect URI it did not register. The id is public (it appears in
// authorize URLs); only the secret is sensitive, and rotating it invalidates
// every registration.

const (
	clientIDPrefix = "mcp_"

	// minSecretLen is the shortest accepted MCP_OAUTH_SECRET, in bytes.
	minSecretLen = 32

	// maxClientIDLen bounds the encoded client_id. It ends up in URLs, hidden
	// form fields and the pending code store, so it must stay small.
	maxClientIDLen = 1500
)

// errClientTooLarge reports a registration whose client_id would exceed
// maxClientIDLen.
var errClientTooLarge = errors.New("client metadata is too large: use fewer or shorter redirect_uris or a shorter client_name")

// clientPayload is the signed content of a client_id.
type clientPayload struct {
	Name         string   `json:"name,omitempty"`
	RedirectURIs []string `json:"redirect_uris"`
	Expiry       int64    `json:"exp"` // Unix seconds
}

// signature returns HMAC-SHA256(secret, body).
func signature(secret []byte, body string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return mac.Sum(nil)
}

// encodeClientID signs p into a client_id.
func encodeClientID(secret []byte, p clientPayload) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	sig := base64.RawURLEncoding.EncodeToString(signature(secret, body))
	id := clientIDPrefix + body + "." + sig
	if len(id) > maxClientIDLen {
		return "", errClientTooLarge
	}
	return id, nil
}

// decodeClientID verifies a client_id and returns the client it describes. It
// fails for malformed, tampered, foreign-secret and expired ids. The redirect
// URIs are validated again even though this server signed them: the policy may
// have tightened since, and a signed value is no reason to trust a URI blindly.
func decodeClientID(secret []byte, id string, now time.Time) (registeredClient, bool) {
	if len(id) > maxClientIDLen {
		return registeredClient{}, false
	}
	rest, ok := strings.CutPrefix(id, clientIDPrefix)
	if !ok {
		return registeredClient{}, false
	}
	body, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return registeredClient{}, false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, signature(secret, body)) {
		return registeredClient{}, false
	}

	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return registeredClient{}, false
	}
	var p clientPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return registeredClient{}, false
	}
	if now.Unix() > p.Expiry {
		return registeredClient{}, false
	}
	if len(p.RedirectURIs) == 0 || len(p.RedirectURIs) > maxRedirectURIs {
		return registeredClient{}, false
	}
	for _, uri := range p.RedirectURIs {
		if _, err := validateRedirectURI(uri); err != nil {
			return registeredClient{}, false
		}
	}
	return registeredClient{
		name:         sanitizeClientName(p.Name),
		redirectURIs: p.RedirectURIs,
		expiry:       time.Unix(p.Expiry, 0),
	}, true
}

// signClient issues a client_id for c, valid for clientTTL from now.
func (s *oauthServer) signClient(c registeredClient) (string, error) {
	return encodeClientID(s.secret, clientPayload{
		Name:         c.name,
		RedirectURIs: c.redirectURIs,
		Expiry:       s.now().Add(clientTTL).Unix(),
	})
}

// lookupClient resolves a client_id to the registration it carries.
func (s *oauthServer) lookupClient(id string) (registeredClient, bool) {
	return decodeClientID(s.secret, id, s.now())
}

// loadOAuthSecret returns the key that signs client_ids. configured is the
// value of MCP_OAUTH_SECRET. When it is missing or shorter than minSecretLen
// bytes a random per-process key is generated instead: nothing breaks, but
// every registration is lost on restart, which warning explains. persistent
// reports whether the key will be the same after a restart.
func loadOAuthSecret(configured string) (secret []byte, persistent bool, warning string) {
	configured = strings.TrimSpace(configured)
	if len(configured) >= minSecretLen {
		return []byte(configured), true, ""
	}

	secret = make([]byte, minSecretLen)
	if _, err := rand.Read(secret); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	if configured == "" {
		return secret, false, "MCP_OAUTH_SECRET is not set: OAuth client registrations use a per-process key and will not survive a restart or redeploy"
	}
	return secret, false, fmt.Sprintf("MCP_OAUTH_SECRET is shorter than %d bytes and was ignored: OAuth client registrations use a per-process key and will not survive a restart or redeploy", minSecretLen)
}
