package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/store"
)

// oauthMCP is an MCP server that demands a bearer token and hosts its own
// OAuth authorization server (metadata, dynamic registration, PKCE, refresh).
type oauthMCP struct {
	*httptest.Server
	mu        sync.Mutex
	challenge string
	access    string
	refreshes int
}

func newOAuthMCP(t *testing.T) *oauthMCP {
	t.Helper()
	m := &oauthMCP{}
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		s := mcp.NewServer(&mcp.Implementation{Name: "secured", Version: "1"}, nil)
		mcp.AddTool(s, &mcp.Tool{Name: "whoami", Description: "x"}, func(context.Context, *mcp.CallToolRequest, echoIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "authorised"}}}, nil, nil
		})
		return s
	}, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true})

	mux := http.NewServeMux()
	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Close)
	js := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		want := "Bearer " + m.access
		m.mu.Unlock()
		if m.access == "" || r.Header.Get("Authorization") != want {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+m.URL+`/.well-known/oauth-protected-resource/mcp"`)
			http.Error(w, "unauthorized", 401)
			return
		}
		inner.ServeHTTP(w, r)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"resource": m.URL + "/mcp", "authorization_servers": []string{m.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"issuer": m.URL, "authorization_endpoint": m.URL + "/authorize", "token_endpoint": m.URL + "/token",
			"registration_endpoint": m.URL + "/register", "response_types_supported": []string{"code"},
			"code_challenge_methods_supported": []string{"S256"}, "jwks_uri": m.URL + "/jwks"})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		js(w, map[string]any{"client_id": "dyn-client", "redirect_uris": []string{}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		m.mu.Lock()
		m.challenge = q.Get("code_challenge")
		m.mu.Unlock()
		if q.Get("resource") != m.URL+"/mcp" {
			http.Error(w, "missing resource", 400)
			return
		}
		u, _ := url.Parse(q.Get("redirect_uri"))
		v := u.Query()
		v.Set("code", "the-code")
		v.Set("state", q.Get("state"))
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), 302)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		m.mu.Lock()
		defer m.mu.Unlock()
		switch r.PostFormValue("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
			if r.PostFormValue("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != m.challenge {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			m.access = "access-1"
			// Already expired, so the gateway must refresh before first use.
			js(w, map[string]any{"access_token": "access-1", "token_type": "Bearer", "refresh_token": "refresh-1", "expires_in": 1})
		case "refresh_token":
			if r.PostFormValue("refresh_token") != "refresh-1" {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			m.refreshes++
			m.access = "access-2"
			js(w, map[string]any{"access_token": "access-2", "token_type": "Bearer", "refresh_token": "refresh-1", "expires_in": 3600})
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, 400)
		}
	})
	return m
}

func TestUpstreamOAuthEndToEnd(t *testing.T) {
	e := newEnv(t)
	up := newOAuthMCP(t)
	ctx := context.Background()
	u := &store.Upstream{UserID: e.user.ID, Slug: "secured", Name: "Secured", URL: up.URL + "/mcp", Transport: "http", AuthType: "oauth", Enabled: true}
	if err := e.st.CreateUpstream(ctx, u); err != nil {
		t.Fatal(err)
	}
	sess, _ := e.st.CreateSession(ctx, e.user.ID, sessionTTL)
	cookie := &http.Cookie{Name: cookieSession, Value: sess}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, body []byte) *http.Response {
		req, _ := http.NewRequest(method, e.ts.URL+path, bytes.NewReader(body))
		req.AddCookie(cookie)
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Before logging in, the MCP is unusable and the gateway skips it without failing.
	tok := "mcpt_" + strings.Repeat("a", 20)
	e.st.CreateAPIToken(ctx, &store.APIToken{UserID: e.user.ID, Name: "t", Prefix: tok[:10]}, tok)
	if names := toolNames(t, connect(t, e.ts.URL+"/mcp", tok)); len(names) != 0 {
		t.Fatalf("unauthenticated upstream exposed tools: %v", names)
	}

	// Start: the gateway discovers the login server, registers itself and builds the URL.
	resp := do("POST", "/api/upstreams/"+u.ID+"/oauth/start", nil)
	var start map[string]string
	json.NewDecoder(resp.Body).Decode(&start)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(start["url"], up.URL+"/authorize") {
		t.Fatalf("start: %d %v", resp.StatusCode, start)
	}
	q, _ := url.Parse(start["url"])
	if q.Query().Get("client_id") != "dyn-client" || q.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("bad authorize URL: %s", start["url"])
	}

	// The user approves at the upstream, which redirects back to the gateway.
	az, _ := noRedirect.Get(start["url"])
	loc, _ := az.Location()
	az.Body.Close()
	if loc == nil || loc.Path != "/oauth/upstream/callback" {
		t.Fatalf("authorize redirected to %v", loc)
	}
	cb := do("GET", loc.RequestURI(), nil)
	cb.Body.Close()
	if got := cb.Header.Get("Location"); !strings.HasPrefix(got, "/mcps?connected=") {
		t.Fatalf("callback redirected to %q", got)
	}

	// Tools now flow through, using the refreshed token.
	names := strings.Join(toolNames(t, connect(t, e.ts.URL+"/mcp", tok)), ",")
	if !strings.Contains(names, "secured__whoami") {
		t.Fatalf("tools after login = %q", names)
	}
	if up.refreshes == 0 {
		t.Fatal("expired token wasn't refreshed")
	}
	// The refreshed token was persisted.
	o, err := e.st.GetUpstreamOAuth(ctx, u.ID)
	if err != nil || !strings.Contains(o.Token, "access-2") {
		t.Fatalf("token not persisted: %v %q", err, o)
	}

	// A callback with someone else's / an unknown state is rejected.
	bad := do("GET", "/oauth/upstream/callback?code=x&state=nope", nil)
	bad.Body.Close()
	if !strings.Contains(bad.Header.Get("Location"), "error=") {
		t.Fatalf("bad state accepted: %q", bad.Header.Get("Location"))
	}
}
