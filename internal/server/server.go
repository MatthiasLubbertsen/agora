// Package server wires the HTTP surface: website API, OAuth, and the MCP gateway.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/config"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/crypto"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/store"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/upstream"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/webui"
)

const (
	settingOpenSignup     = "open_signup"
	settingAllowAnonymous = "allow_anonymous"
	cookieSession         = "gw_session"
)

type Server struct {
	cfg   *config.Config
	st    *store.Store
	box   *crypto.Box
	up    *upstream.Manager
	log   *slog.Logger
	oidc  *oidcAuth
	setup string // one-time admin bootstrap token; empty once an admin exists

	regLimit  *limiter
	authLimit *limiter
}

func New(cfg *config.Config, st *store.Store, box *crypto.Box, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, st: st, box: box, log: log,
		up:        upstream.NewManager(st, cfg.AllowPrivateUpstreams),
		regLimit:  newLimiter(30, time.Hour),
		authLimit: newLimiter(60, time.Minute),
	}
	s.oidc = newOIDC(cfg, box)
	n, err := st.AdminCount(context.Background())
	if err != nil {
		return nil, err
	}
	if n == 0 {
		s.setup = crypto.RandToken(18)
	}
	return s, nil
}

// SetupURL is non-empty while the instance still needs its first admin.
func (s *Server) SetupURL() string {
	if s.setup == "" {
		return ""
	}
	return s.cfg.BaseURL + "/setup?token=" + url.QueryEscape(s.setup)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Website login.
	mux.HandleFunc("GET /auth/login/{provider}", s.handleLogin)
	mux.HandleFunc("GET /auth/callback/{provider}", s.handleCallback)

	// JSON API for the website.
	api := http.NewServeMux()
	api.HandleFunc("GET /api/auth/providers", s.handleProviders)
	api.HandleFunc("POST /api/auth/logout", s.auth(s.handleLogout))
	api.HandleFunc("GET /api/me", s.auth(s.handleMe))
	api.HandleFunc("GET /api/upstreams", s.auth(s.handleUpstreamList))
	api.HandleFunc("POST /api/upstreams", s.auth(s.handleUpstreamCreate))
	api.HandleFunc("PATCH /api/upstreams/{id}", s.auth(s.handleUpstreamUpdate))
	api.HandleFunc("DELETE /api/upstreams/{id}", s.auth(s.handleUpstreamDelete))
	api.HandleFunc("POST /api/upstreams/{id}/test", s.auth(s.handleUpstreamTest))
	api.HandleFunc("GET /api/upstreams/{id}/catalog", s.auth(s.handleUpstreamCatalog))
	api.HandleFunc("POST /api/upstreams/{id}/oauth/start", s.auth(s.handleUpstreamOAuthStart))
	api.HandleFunc("POST /api/upstreams/{id}/oauth/disconnect", s.auth(s.handleUpstreamOAuthDisconnect))
	api.HandleFunc("GET /api/tokens", s.auth(s.handleTokenList))
	api.HandleFunc("POST /api/tokens", s.auth(s.handleTokenCreate))
	api.HandleFunc("DELETE /api/tokens/{id}", s.auth(s.handleTokenDelete))
	api.HandleFunc("GET /api/grants", s.auth(s.handleGrantList))
	api.HandleFunc("DELETE /api/grants/{client_id}", s.auth(s.handleGrantRevoke))
	api.HandleFunc("GET /api/admin/users", s.admin(s.handleAdminUsers))
	api.HandleFunc("POST /api/admin/users", s.admin(s.handleAdminUserCreate))
	api.HandleFunc("PATCH /api/admin/users/{id}", s.admin(s.handleAdminUserUpdate))
	api.HandleFunc("DELETE /api/admin/users/{id}", s.admin(s.handleAdminUserDelete))
	api.HandleFunc("GET /api/admin/settings", s.admin(s.handleSettingsGet))
	api.HandleFunc("PUT /api/admin/settings", s.admin(s.handleSettingsPut))
	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "not found") })
	mux.Handle("/api/", s.csrf(api))
	mux.HandleFunc("GET /oauth/upstream/callback", s.auth(s.handleUpstreamOAuthCallback))

	// The gateway itself, and the OAuth server MCP clients use to log in.
	mux.Handle("/mcp", s.gatewayHandler(false))
	mux.Handle("/mcp/public", s.gatewayHandler(true))
	s.mountOAuthServer(mux)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("/", webui.Handler())

	return s.secureHeaders(s.recoverer(s.logRequests(mux)))
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeErr(w, 400, "invalid request body")
		return false
	}
	return true
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" && !strings.HasPrefix(r.URL.Path, "/assets/") {
			s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.code, "ms", time.Since(start).Milliseconds())
		}
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil && v != http.ErrAbortHandler {
				s.log.Error("panic", "path", r.URL.Path, "panic", v)
				writeErr(w, 500, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/mcp") && !strings.HasPrefix(r.URL.Path, "/oauth/") && !strings.HasPrefix(r.URL.Path, "/.well-known/") {
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
		}
		if s.cfg.Secure() {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// csrf rejects state-changing browser requests that come from another origin.
// Combined with SameSite=Lax cookies this closes cross-site request forgery.
func (s *Server) csrf(next http.Handler) http.Handler {
	base, _ := url.Parse(s.cfg.BaseURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				if ou, err := url.Parse(o); err != nil || ou.Host != base.Host {
					writeErr(w, 403, "cross-origin request blocked")
					return
				}
			}
			if sf := r.Header.Get("Sec-Fetch-Site"); sf != "" && sf != "same-origin" && sf != "none" {
				writeErr(w, 403, "cross-origin request blocked")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

var errNoAuth = errors.New("unauthorized")
