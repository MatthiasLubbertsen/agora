package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/matthiaslubbertsen/mcp-full-off-mcps/internal/crypto"
	"github.com/matthiaslubbertsen/mcp-full-off-mcps/internal/store"
)

const (
	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	codeTTL    = 5 * time.Minute
)

// The gateway is its own OAuth 2.1 authorization server for MCP clients
// (Claude, Cursor, ...): metadata discovery, dynamic client registration,
// authorization code + PKCE, and rotating refresh tokens. Users prove who
// they are with the website login.
func (s *Server) mountOAuthServer(mux *http.ServeMux) {
	meta := func(w http.ResponseWriter, r *http.Request, v any) {
		corsMCP(w, r)
		if r.Method == http.MethodOptions {
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		writeJSON(w, 200, v)
	}
	prm := map[string]any{
		"resource":                 s.cfg.BaseURL + "/mcp",
		"authorization_servers":    []string{s.cfg.BaseURL},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"mcp"},
		"resource_name":            "MCP Full Of MCPs",
	}
	asm := map[string]any{
		"issuer":                                s.cfg.BaseURL,
		"authorization_endpoint":                s.cfg.BaseURL + "/oauth/authorize",
		"token_endpoint":                        s.cfg.BaseURL + "/oauth/token",
		"registration_endpoint":                 s.cfg.BaseURL + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                      []string{"mcp"},
	}
	for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		mux.HandleFunc("GET "+p, func(w http.ResponseWriter, r *http.Request) { meta(w, r, prm) })
		mux.HandleFunc("OPTIONS "+p, func(w http.ResponseWriter, r *http.Request) { meta(w, r, nil) })
	}
	for _, p := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/mcp"} {
		mux.HandleFunc("GET "+p, func(w http.ResponseWriter, r *http.Request) { meta(w, r, asm) })
		mux.HandleFunc("OPTIONS "+p, func(w http.ResponseWriter, r *http.Request) { meta(w, r, nil) })
	}

	mux.HandleFunc("POST /oauth/register", s.handleRegister)
	mux.HandleFunc("OPTIONS /oauth/register", func(w http.ResponseWriter, r *http.Request) { corsMCP(w, r) })
	mux.HandleFunc("POST /oauth/token", s.handleToken)
	mux.HandleFunc("OPTIONS /oauth/token", func(w http.ResponseWriter, r *http.Request) { corsMCP(w, r) })
	// The browser lands here; the website shows the consent screen.
	mux.HandleFunc("GET /oauth/authorize", s.handleAuthorizeRedirect)
	mux.Handle("GET /api/oauth/authorize", s.csrf(http.HandlerFunc(s.auth(s.handleAuthorizeInfo))))
	mux.Handle("POST /api/oauth/authorize", s.csrf(http.HandlerFunc(s.auth(s.handleAuthorizeDecide))))
}

func oauthErr(w http.ResponseWriter, code int, e, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, code, map[string]string{"error": e, "error_description": desc})
}

func validRedirectURI(u string) bool {
	pu, err := url.Parse(u)
	if err != nil || pu.Fragment != "" || pu.User != nil {
		return false
	}
	switch strings.ToLower(pu.Scheme) {
	case "https":
		return pu.Host != ""
	case "http":
		h := pu.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	case "", "javascript", "data", "file", "vbscript", "about", "blob", "ftp", "ws", "wss":
		return false
	}
	return true // custom app schemes such as cursor:// or vscode://
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	corsMCP(w, r)
	if !s.regLimit.allow(s.clientIP(r)) {
		oauthErr(w, 429, "temporarily_unavailable", "too many registrations")
		return
	}
	var in struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 {
		oauthErr(w, 400, "invalid_client_metadata", "redirect_uris is required")
		return
	}
	for _, u := range in.RedirectURIs {
		if !validRedirectURI(u) {
			oauthErr(w, 400, "invalid_redirect_uri", "unsupported redirect URI: "+u)
			return
		}
	}
	name := strings.TrimSpace(in.ClientName)
	if name == "" {
		name = "Unnamed app"
	}
	if len(name) > 100 {
		name = name[:100]
	}
	c := &store.OAuthClient{ClientID: "mcpc_" + crypto.RandToken(18), Name: name, RedirectURIs: in.RedirectURIs}
	resp := map[string]any{
		"client_id": c.ClientID, "client_name": name, "redirect_uris": in.RedirectURIs,
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"client_id_issued_at": time.Now().Unix(),
	}
	if in.AuthMethod == "client_secret_post" || in.AuthMethod == "client_secret_basic" {
		secret := crypto.RandToken(32)
		c.SecretHash = crypto.Hash(secret)
		resp["client_secret"], resp["client_secret_expires_at"] = secret, 0
		resp["token_endpoint_auth_method"] = in.AuthMethod
	} else {
		resp["token_endpoint_auth_method"] = "none"
	}
	if err := s.st.CreateOAuthClient(r.Context(), c); err != nil {
		oauthErr(w, 500, "server_error", "could not register")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, resp)
}

func (s *Server) handleAuthorizeRedirect(w http.ResponseWriter, r *http.Request) {
	// Everything is validated again on the consent screen's API call.
	http.Redirect(w, r, "/consent?"+r.URL.RawQuery, http.StatusFound)
}

type authzParams struct {
	ClientID, RedirectURI, State, Challenge string
}

// parseAuthz validates an authorization request. Errors that must not be
// redirected back to the client (bad client or redirect URI) are returned as
// msg; other errors are returned as an OAuth error code for redirection.
func (s *Server) parseAuthz(r *http.Request, q url.Values) (*authzParams, *store.OAuthClient, string, string) {
	c, err := s.st.GetOAuthClient(r.Context(), q.Get("client_id"))
	if err != nil {
		return nil, nil, "", "Unknown application. Remove and re-add the MCP server in your app."
	}
	ru := q.Get("redirect_uri")
	ok := false
	for _, u := range c.RedirectURIs {
		if subtle.ConstantTimeCompare([]byte(u), []byte(ru)) == 1 {
			ok = true
		}
	}
	if !ok {
		return nil, c, "", "The redirect address doesn't match what this application registered."
	}
	p := &authzParams{ClientID: c.ClientID, RedirectURI: ru, State: q.Get("state"), Challenge: q.Get("code_challenge")}
	if q.Get("response_type") != "code" {
		return p, c, "unsupported_response_type", ""
	}
	if p.Challenge == "" || q.Get("code_challenge_method") != "S256" {
		return p, c, "invalid_request", ""
	}
	return p, c, "", ""
}

func redirectWith(base string, kv map[string]string) string {
	u, _ := url.Parse(base)
	q := u.Query()
	for k, v := range kv {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) handleAuthorizeInfo(w http.ResponseWriter, r *http.Request) {
	p, c, code, msg := s.parseAuthz(r, r.URL.Query())
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if code != "" {
		writeJSON(w, 200, map[string]any{"redirect": redirectWith(p.RedirectURI, map[string]string{"error": code, "state": p.State})})
		return
	}
	pu, _ := url.Parse(p.RedirectURI)
	host := pu.Host
	if host == "" {
		host = pu.Scheme + "://"
	}
	writeJSON(w, 200, map[string]any{"client_name": c.Name, "redirect_host": host, "user": userFrom(r).Email})
}

func (s *Server) handleAuthorizeDecide(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Approve bool              `json:"approve"`
		Params  map[string]string `json:"params"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	q := url.Values{}
	for k, v := range in.Params {
		q.Set(k, v)
	}
	p, _, code, msg := s.parseAuthz(r, q)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if code != "" {
		writeJSON(w, 200, map[string]any{"redirect": redirectWith(p.RedirectURI, map[string]string{"error": code, "state": p.State})})
		return
	}
	if !in.Approve {
		writeJSON(w, 200, map[string]any{"redirect": redirectWith(p.RedirectURI, map[string]string{"error": "access_denied", "state": p.State})})
		return
	}
	authCode := crypto.RandToken(32)
	if err := s.st.CreateOAuthCode(r.Context(), authCode, &store.OAuthCode{ClientID: p.ClientID, UserID: userFrom(r).ID,
		RedirectURI: p.RedirectURI, CodeChallenge: p.Challenge}, codeTTL); err != nil {
		writeErr(w, 500, "couldn't authorise")
		return
	}
	writeJSON(w, 200, map[string]any{"redirect": redirectWith(p.RedirectURI, map[string]string{"code": authCode, "state": p.State})})
}

func (s *Server) authenticateClient(r *http.Request, c *store.OAuthClient) bool {
	if c.SecretHash == "" {
		return true // public client: PKCE is the proof
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	return id == c.ClientID && secret != "" && subtle.ConstantTimeCompare([]byte(crypto.Hash(secret)), []byte(c.SecretHash)) == 1
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	corsMCP(w, r)
	if !s.authLimit.allow("tok:" + s.clientIP(r)) {
		oauthErr(w, 429, "temporarily_unavailable", "slow down")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := r.ParseForm(); err != nil {
		oauthErr(w, 400, "invalid_request", "bad form body")
		return
	}
	clientID := r.PostFormValue("client_id")
	if id, _, ok := r.BasicAuth(); ok {
		clientID = id
	}
	c, err := s.st.GetOAuthClient(r.Context(), clientID)
	if err != nil || !s.authenticateClient(r, c) {
		oauthErr(w, 401, "invalid_client", "unknown client or bad secret")
		return
	}
	var userID string
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		code, err := s.st.ConsumeOAuthCode(r.Context(), r.PostFormValue("code"))
		if err != nil || code.ClientID != c.ClientID || code.RedirectURI != r.PostFormValue("redirect_uri") {
			oauthErr(w, 400, "invalid_grant", "code is invalid, expired or already used")
			return
		}
		sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		if r.PostFormValue("code_verifier") == "" ||
			subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(code.CodeChallenge)) != 1 {
			oauthErr(w, 400, "invalid_grant", "PKCE verification failed")
			return
		}
		userID = code.UserID
	case "refresh_token":
		cid, uid, err := s.st.RotateRefresh(r.Context(), r.PostFormValue("refresh_token"))
		if err != nil || cid != c.ClientID {
			oauthErr(w, 400, "invalid_grant", "refresh token is invalid or expired")
			return
		}
		userID = uid
	default:
		oauthErr(w, 400, "unsupported_grant_type", "use authorization_code or refresh_token")
		return
	}
	if u, err := s.st.UserByID(r.Context(), userID); err != nil || u.Disabled {
		oauthErr(w, 400, "invalid_grant", "account unavailable")
		return
	}
	access, refresh := "mcpo_"+crypto.RandToken(32), "mcpr_"+crypto.RandToken(32)
	if err := s.st.CreateOAuthToken(r.Context(), access, refresh, c.ClientID, userID, accessTTL, refreshTTL); err != nil {
		oauthErr(w, 500, "server_error", "could not issue token")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(accessTTL.Seconds()),
		"refresh_token": refresh, "scope": "mcp"})
}
