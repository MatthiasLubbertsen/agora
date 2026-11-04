package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/config"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/crypto"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/store"
)

const sessionTTL = 30 * 24 * time.Hour

type oidcProvider struct {
	cfg      config.Provider
	mu       sync.Mutex
	provider *oidc.Provider
}

type oidcAuth struct {
	base      string
	box       *crypto.Box
	providers map[string]*oidcProvider
	order     []string
}

func newOIDC(cfg *config.Config, box *crypto.Box) *oidcAuth {
	o := &oidcAuth{base: cfg.BaseURL, box: box, providers: map[string]*oidcProvider{}}
	for _, p := range cfg.Providers {
		o.providers[p.ID] = &oidcProvider{cfg: p}
		o.order = append(o.order, p.ID)
	}
	return o
}

// get lazily performs OIDC discovery so a provider being down at boot is not fatal.
func (p *oidcProvider) get(ctx context.Context) (*oidc.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil {
		return p.provider, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pr, err := oidc.NewProvider(ctx, p.cfg.Issuer)
	if err != nil {
		return nil, err
	}
	p.provider = pr
	return pr, nil
}

func (o *oidcAuth) oauthConfig(p *oidcProvider, pr *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{ClientID: p.cfg.ClientID, ClientSecret: p.cfg.ClientSecret, Endpoint: pr.Endpoint(),
		RedirectURL: o.base + "/auth/callback/" + p.cfg.ID, Scopes: p.cfg.Scopes}
}

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
	Setup    string `json:"u,omitempty"`
	Expires  int64  `json:"e"`
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	type pv struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	out := []pv{}
	for _, id := range s.oidc.order {
		out = append(out, pv{id, s.oidc.providers[id].cfg.Label})
	}
	writeJSON(w, 200, map[string]any{"providers": out, "needs_setup": s.setup != ""})
}

func safeNext(n string) string {
	if n == "" || !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.HasPrefix(n, "/\\") {
		return "/"
	}
	return n
}

func (s *Server) loginError(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit.allow("login:" + s.clientIP(r)) {
		s.loginError(w, r, "Too many attempts, wait a minute.")
		return
	}
	p := s.oidc.providers[r.PathValue("provider")]
	if p == nil {
		http.NotFound(w, r)
		return
	}
	pr, err := p.get(r.Context())
	if err != nil {
		s.log.Error("oidc discovery", "provider", p.cfg.ID, "err", err)
		s.loginError(w, r, "Can't reach the login provider right now.")
		return
	}
	ls := loginState{State: crypto.RandToken(18), Nonce: crypto.RandToken(18), Verifier: oauth2.GenerateVerifier(),
		Next: safeNext(r.URL.Query().Get("next")), Setup: r.URL.Query().Get("setup"), Expires: time.Now().Add(10 * time.Minute).Unix()}
	raw, _ := json.Marshal(ls)
	http.SetCookie(w, &http.Cookie{Name: "gw_login", Value: s.box.Seal(string(raw)), Path: "/auth", MaxAge: 600,
		HttpOnly: true, Secure: s.cfg.Secure(), SameSite: http.SameSiteLaxMode})
	cfg := s.oidc.oauthConfig(p, pr)
	http.Redirect(w, r, cfg.AuthCodeURL(ls.State, oidc.Nonce(ls.Nonce), oauth2.S256ChallengeOption(ls.Verifier)), http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit.allow("cb:" + s.clientIP(r)) {
		s.loginError(w, r, "Too many attempts, wait a minute.")
		return
	}
	p := s.oidc.providers[r.PathValue("provider")]
	if p == nil {
		http.NotFound(w, r)
		return
	}
	c, err := r.Cookie("gw_login")
	if err != nil {
		s.loginError(w, r, "Login expired, try again.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "gw_login", Path: "/auth", MaxAge: -1})
	plain, err := s.box.Open(c.Value)
	var ls loginState
	if err != nil || json.Unmarshal([]byte(plain), &ls) != nil || time.Now().Unix() > ls.Expires {
		s.loginError(w, r, "Login expired, try again.")
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.loginError(w, r, "The provider refused the login: "+e)
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(ls.State)) != 1 {
		s.loginError(w, r, "Login state mismatch, try again.")
		return
	}
	pr, err := p.get(r.Context())
	if err != nil {
		s.loginError(w, r, "Can't reach the login provider right now.")
		return
	}
	cfg := s.oidc.oauthConfig(p, pr)
	tok, err := cfg.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(ls.Verifier))
	if err != nil {
		s.log.Error("oidc exchange", "err", err)
		s.loginError(w, r, "Login failed.")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		s.loginError(w, r, "The provider didn't return an ID token.")
		return
	}
	idt, err := pr.Verifier(&oidc.Config{ClientID: p.cfg.ClientID}).Verify(r.Context(), rawID)
	if err != nil || subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(ls.Nonce)) != 1 {
		s.loginError(w, r, "Couldn't verify the login.")
		return
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
		Name          string `json:"name"`
		Username      string `json:"preferred_username"`
	}
	_ = idt.Claims(&claims)
	if claims.Email == "" {
		// Some providers keep claims out of the ID token.
		if ui, err := pr.UserInfo(r.Context(), cfg.TokenSource(r.Context(), tok)); err == nil {
			var u struct {
				Email         string `json:"email"`
				EmailVerified *bool  `json:"email_verified"`
				Name          string `json:"name"`
				Username      string `json:"preferred_username"`
			}
			if ui.Claims(&u) == nil {
				claims.Email, claims.EmailVerified, claims.Name, claims.Username = u.Email, u.EmailVerified, u.Name, u.Username
			}
		}
	}
	claims.Email = strings.ToLower(strings.TrimSpace(claims.Email))
	if claims.Email == "" || !strings.Contains(claims.Email, "@") {
		s.loginError(w, r, "The provider didn't share an email address.")
		return
	}
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		s.loginError(w, r, "Your email address isn't verified with the provider.")
		return
	}
	name := claims.Name
	if name == "" {
		name = claims.Username
	}

	u, err := s.resolveUser(r.Context(), claims.Email, name, ls.Setup)
	if err != nil {
		s.loginError(w, r, err.Error())
		return
	}
	sess, err := s.st.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		s.loginError(w, r, "Couldn't start a session.")
		return
	}
	_ = s.st.TouchLogin(r.Context(), u.ID, name)
	http.SetCookie(w, &http.Cookie{Name: cookieSession, Value: sess, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: s.cfg.Secure(), SameSite: http.SameSiteLaxMode})
	next := ls.Next
	if ls.Setup != "" {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusFound)
}

// resolveUser applies the sign-in rules: known users log in, the one-time setup
// token creates the first admin, and unknown users are admitted only when
// open signup is on.
func (s *Server) resolveUser(ctx context.Context, email, name, setupToken string) (*store.User, error) {
	if setupToken != "" {
		if s.setup == "" || subtle.ConstantTimeCompare([]byte(setupToken), []byte(s.setup)) != 1 {
			return nil, errors.New("This setup link is no longer valid.")
		}
		if n, _ := s.st.AdminCount(ctx); n > 0 {
			s.setup = ""
			return nil, errors.New("An admin already exists.")
		}
		u, err := s.st.UserByEmail(ctx, email)
		if errors.Is(err, store.ErrNotFound) {
			u, err = s.st.CreateUser(ctx, email, name, true)
		} else if err == nil {
			t := true
			f := false
			err = s.st.UpdateUser(ctx, u.ID, nil, &t, &f)
		}
		if err != nil {
			return nil, errors.New("Couldn't create the admin account.")
		}
		s.setup = ""
		s.log.Info("first admin created", "email", email)
		return u, nil
	}
	u, err := s.st.UserByEmail(ctx, email)
	switch {
	case err == nil:
		if u.Disabled {
			return nil, errors.New("This account is disabled.")
		}
		return u, nil
	case errors.Is(err, store.ErrNotFound):
		if !s.st.GetBool(ctx, settingOpenSignup) {
			return nil, fmt.Errorf("%s hasn't been invited. Ask an admin to add your email.", email)
		}
		u, err = s.st.CreateUser(ctx, email, name, false)
		if err != nil {
			return nil, errors.New("Couldn't create your account.")
		}
		return u, nil
	default:
		return nil, errors.New("Something went wrong.")
	}
}

// ---- session middleware ----

type ctxKey int

const userKey ctxKey = 1

func (s *Server) sessionUser(r *http.Request) *store.User {
	c, err := r.Cookie(cookieSession)
	if err != nil || c.Value == "" {
		return nil
	}
	u, err := s.st.SessionUser(r.Context(), c.Value)
	if err != nil {
		return nil
	}
	return u
}

func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

// auth requires a logged-in user.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.sessionUser(r)
		if u == nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeErr(w, 401, "not logged in")
			} else {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			}
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	}
}

// admin requires a logged-in admin.
func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r).IsAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		next(w, r)
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieSession); err == nil {
		_ = s.st.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieSession, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.Secure(), SameSite: http.SameSiteLaxMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	ctx := r.Context()
	writeJSON(w, 200, map[string]any{
		"user":            u,
		"mcp_url":         s.cfg.BaseURL + "/mcp",
		"public_mcp_url":  s.cfg.BaseURL + "/mcp/public",
		"allow_anonymous": s.st.GetBool(ctx, settingAllowAnonymous),
	})
}
