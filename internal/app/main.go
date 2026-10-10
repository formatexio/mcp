package app

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	mcpserver "github.com/formatexio/mcp/internal/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is the server version, shared with the MCP initialize handshake.
const version = mcpserver.Version

func Main() {
	var (
		httpAddr    = flag.String("http", "", "Enable HTTP mode and listen on this address (e.g. :9004). Omit for stdio mode.")
		baseURL     = flag.String("base-url", "", "Public base URL of this MCP server for OAuth (overrides MCP_BASE_URL, e.g. https://mcp.formatex.io)")
		apiURL      = flag.String("api-url", "", "FormaTeX API base URL (overrides FORMATEX_API_URL)")
		apiKey      = flag.String("api-key", "", "FormaTeX API key (overrides FORMATEX_API_KEY, stdio mode only)")
		showVersion = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("formatex-mcp v%s\n", version)
		os.Exit(0)
	}

	resolvedAPIURL := resolveConfig(*apiURL, "FORMATEX_API_URL", "https://api.formatex.io/")
	resolvedBaseURL := resolveConfig(*baseURL, "MCP_BASE_URL", "")

	if *httpAddr != "" {
		// HTTP mode: no server-level API key needed — each request carries the user's key
		runHTTP(resolvedAPIURL, resolvedBaseURL, *httpAddr)
	} else {
		// stdio mode: API key required at startup
		resolvedAPIKey := resolveConfig(*apiKey, "FORMATEX_API_KEY", "")
		if resolvedAPIKey == "" {
			log.Fatal("FORMATEX_API_KEY is required. Set the FORMATEX_API_KEY environment variable or use --api-key.")
		}
		client := mcpserver.NewClient(resolvedAPIURL, resolvedAPIKey)
		server := mcpserver.NewMCPServer(client)
		runStdio(server)
	}
}

func runStdio(server *sdk.Server) {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := server.Run(ctx, &sdk.StdioTransport{}); err != nil {
		log.Fatalf("MCP server error: %v", err)
	}
}

// newHTTPMux builds the HTTP routes: the MCP endpoint behind the API-key check,
// a health probe and, when baseURL is set, the OAuth 2.0 endpoints that remote
// MCP clients need (RFC 9728 resource metadata, RFC 8414 server metadata,
// dynamic registration, authorize and token). secret signs OAuth client ids and
// is only used when baseURL is set.
func newHTTPMux(apiURL, baseURL string, secret []byte) http.Handler {
	baseURL = strings.TrimRight(baseURL, "/")

	handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		log.Printf("[mcp] factory called: %s %s", r.Method, r.URL.Path)
		key := extractAPIKey(r)
		client := mcpserver.NewClient(apiURL, key)
		return mcpserver.NewMCPServer(client)
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", requireAPIKey(baseURL, handler))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","service":"formatex-mcp","version":"`+version+`"}`)
	})

	if baseURL != "" {
		newOAuthServer(baseURL, secret).registerRoutes(mux)
	}
	return mux
}

func runHTTP(apiURL, baseURL, addr string) {
	baseURL = strings.TrimRight(baseURL, "/")

	var secret []byte
	if baseURL != "" {
		var warning string
		secret, _, warning = loadOAuthSecret(resolveConfig("", "MCP_OAUTH_SECRET", ""))
		if warning != "" {
			log.Printf("WARNING: %s", warning)
		}
		log.Printf("OAuth 2.0 enabled (base URL: %s)", baseURL)
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           loggingMiddleware(recoveryMiddleware(newHTTPMux(apiURL, baseURL, secret))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		log.Printf("formatex-mcp v%s listening on %s (HTTP/Streamable mode)", version, addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("Shutting down...")
	if err := httpServer.Shutdown(context.Background()); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
}

// loggingMiddleware logs method, path, status code, and duration for every request.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(lrw, r)
		log.Printf("[http] %s %s -> %d (%s)", r.Method, r.URL.Path, lrw.status, time.Since(start))
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.status = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Flush() {
	if f, ok := lrw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// recoveryMiddleware catches panics, logs the stack trace, and returns 500.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[panic] %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":"internal server error"}`)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// extractAPIKey reads the API key from the request.
// Checks Authorization: Bearer <key> first, then X-API-Key: <key>.
func extractAPIKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
	}
	return r.Header.Get("X-API-Key")
}

// bearerChallenge builds the WWW-Authenticate value for a 401 from the MCP
// endpoint. When the public base URL is known it points clients at the RFC 9728
// protected resource metadata so they can discover the authorization server.
func bearerChallenge(baseURL string) string {
	challenge := `Bearer realm="formatex-mcp"`
	if baseURL = strings.TrimRight(baseURL, "/"); baseURL != "" {
		challenge += `, resource_metadata="` + baseURL + `/.well-known/oauth-protected-resource"`
	}
	return challenge
}

// requireAPIKey is a middleware that returns 401 with a WWW-Authenticate
// challenge if no API key is present in the request.
func requireAPIKey(baseURL string, next http.Handler) http.Handler {
	challenge := bearerChallenge(baseURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if extractAPIKey(r) == "" {
			w.Header().Set("WWW-Authenticate", challenge)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"API key required. Provide Authorization: Bearer <key> or X-API-Key: <key> header."}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func resolveConfig(flagVal, envVar, defaultVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return defaultVal
}
