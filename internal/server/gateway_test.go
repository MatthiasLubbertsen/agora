package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/config"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/crypto"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/store"
)

type echoIn struct {
	Text string `json:"text"`
}

func mockUpstream(t *testing.T, tool string) *httptest.Server {
	t.Helper()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		s := mcp.NewServer(&mcp.Implementation{Name: "mock", Version: "1"}, nil)
		mcp.AddTool(s, &mcp.Tool{Name: tool, Description: "echo"}, func(ctx context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + in.Text}}}, nil, nil
		})
		s.AddPrompt(&mcp.Prompt{Name: "hello"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "hi"}}}}, nil
		})
		return s
	}, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

type env struct {
	srv  *Server
	ts   *httptest.Server
	st   *store.Store
	user *store.User
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	box, _ := crypto.NewBox("test")
	st, err := store.Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	t.Cleanup(ts.Close)
	cfg := &config.Config{BaseURL: ts.URL, AllowPrivateUpstreams: true, DataDir: dir}
	srv, err := New(cfg, st, box, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = srv.Handler()
	u, err := st.CreateUser(context.Background(), "me@example.com", "Me", true)
	if err != nil {
		t.Fatal(err)
	}
	return &env{srv, ts, st, u}
}

func (e *env) addUpstream(t *testing.T, slug, url string, public bool) *store.Upstream {
	t.Helper()
	up := &store.Upstream{UserID: e.user.ID, Slug: slug, Name: slug, URL: url, Transport: "http", AuthType: "none", Enabled: true, IsPublic: public}
	if err := e.st.CreateUpstream(context.Background(), up); err != nil {
		t.Fatal(err)
	}
	return up
}

type bearerRT struct{ tok string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.tok != "" {
		r.Header.Set("Authorization", "Bearer "+b.tok)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, url, tok string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearerRT{tok}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	var out []string
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tool.Name)
	}
	return out
}

func TestBearerTokenMergesUpstreams(t *testing.T) {
	e := newEnv(t)
	a, b := mockUpstream(t, "echo"), mockUpstream(t, "echo")
	upA := e.addUpstream(t, "alpha", a.URL, false)
	e.addUpstream(t, "beta", b.URL, false)

	tok := "mcpt_" + crypto.RandToken(16)
	if err := e.st.CreateAPIToken(context.Background(), &store.APIToken{UserID: e.user.ID, Name: "t", Prefix: tok[:10]}, tok); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, e.ts.URL+"/mcp", tok)
	names := strings.Join(toolNames(t, cs), ",")
	if !strings.Contains(names, "alpha__echo") || !strings.Contains(names, "beta__echo") {
		t.Fatalf("tools = %s", names)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "beta__echo", Arguments: map[string]any{"text": "yo"}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if got := res.Content[0].(*mcp.TextContent).Text; got != "echo:yo" {
		t.Fatalf("got %q", got)
	}
	if _, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "alpha__hello"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// A token limited to one MCP only sees that one.
	scoped := "mcpt_" + crypto.RandToken(16)
	e.st.CreateAPIToken(context.Background(), &store.APIToken{UserID: e.user.ID, Name: "s", Prefix: scoped[:10], UpstreamIDs: []string{upA.ID}}, scoped)
	names = strings.Join(toolNames(t, connect(t, e.ts.URL+"/mcp", scoped)), ",")
	if strings.Contains(names, "beta__") || !strings.Contains(names, "alpha__echo") {
		t.Fatalf("scoped tools = %s", names)
	}
}

func TestRejectsMissingOrBadAuth(t *testing.T) {
	e := newEnv(t)
	for _, auth := range []string{"", "Bearer mcpt_nope", "Bearer mcpo_nope", "Basic abc"} {
		req, _ := http.NewRequest("POST", e.ts.URL+"/mcp", strings.NewReader(`{}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata") {
			t.Fatalf("auth %q: status %d, header %q", auth, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
	}
}

func TestPublicEndpointOnlyShowsPublicWhenEnabled(t *testing.T) {
	e := newEnv(t)
	up := mockUpstream(t, "echo")
	e.addUpstream(t, "private", up.URL, false)
	e.addUpstream(t, "shared", up.URL, true)

	resp, _ := http.Post(e.ts.URL+"/mcp/public", "application/json", strings.NewReader(`{}`))
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("disabled public endpoint returned %d", resp.StatusCode)
	}
	e.st.SetSetting(context.Background(), settingAllowAnonymous, "true")
	names := strings.Join(toolNames(t, connect(t, e.ts.URL+"/mcp/public", "")), ",")
	if strings.Contains(names, "private__") || !strings.Contains(names, "shared__echo") {
		t.Fatalf("public tools = %s", names)
	}
}

func TestOAuthFlowEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.addUpstream(t, "alpha", mockUpstream(t, "echo").URL, false)
	ctx := context.Background()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Discovery.
	resp, _ := http.Get(e.ts.URL + "/.well-known/oauth-protected-resource/mcp")
	var prm map[string]any
	json.NewDecoder(resp.Body).Decode(&prm)
	resp.Body.Close()
	if prm["resource"] != e.ts.URL+"/mcp" {
		t.Fatalf("prm = %v", prm)
	}

	// Dynamic client registration.
	redirect := "http://localhost:9999/cb"
	body, _ := json.Marshal(map[string]any{"client_name": "Test App", "redirect_uris": []string{redirect}})
	resp, _ = http.Post(e.ts.URL+"/oauth/register", "application/json", bytes.NewReader(body))
	var reg map[string]any
	json.NewDecoder(resp.Body).Decode(&reg)
	resp.Body.Close()
	clientID, _ := reg["client_id"].(string)
	if resp.StatusCode != 201 || clientID == "" {
		t.Fatalf("register: %d %v", resp.StatusCode, reg)
	}
	bad, _ := json.Marshal(map[string]any{"redirect_uris": []string{"javascript:alert(1)"}})
	resp, _ = http.Post(e.ts.URL+"/oauth/register", "application/json", bytes.NewReader(bad))
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("javascript: redirect accepted (%d)", resp.StatusCode)
	}

	verifier := crypto.RandToken(32)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	params := map[string]string{"client_id": clientID, "redirect_uri": redirect, "response_type": "code", "state": "xyz",
		"code_challenge": challenge, "code_challenge_method": "S256"}

	// Not logged in: the consent API refuses.
	pj, _ := json.Marshal(map[string]any{"approve": true, "params": params})
	resp, _ = http.Post(e.ts.URL+"/api/oauth/authorize", "application/json", bytes.NewReader(pj))
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous consent returned %d", resp.StatusCode)
	}

	// Logged in: approve.
	sess, _ := e.st.CreateSession(ctx, e.user.ID, sessionTTL)
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/oauth/authorize", bytes.NewReader(pj))
	req.AddCookie(&http.Cookie{Name: cookieSession, Value: sess})
	resp, _ = noRedirect.Do(req)
	var dec map[string]string
	json.NewDecoder(resp.Body).Decode(&dec)
	resp.Body.Close()
	ru, _ := url.Parse(dec["redirect"])
	code := ru.Query().Get("code")
	if code == "" || ru.Query().Get("state") != "xyz" {
		t.Fatalf("decision = %v", dec)
	}

	token := func(form url.Values) (int, map[string]any) {
		resp, err := http.PostForm(e.ts.URL+"/oauth/token", form)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	// Wrong verifier fails (and burns the code).
	if c, _ := token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {redirect}, "code_verifier": {"wrong"}}); c != 400 {
		t.Fatalf("bad verifier accepted: %d", c)
	}
	// Fresh code, correct verifier.
	resp, _ = noRedirect.Do(func() *http.Request {
		r, _ := http.NewRequest("POST", e.ts.URL+"/api/oauth/authorize", bytes.NewReader(pj))
		r.AddCookie(&http.Cookie{Name: cookieSession, Value: sess})
		return r
	}())
	json.NewDecoder(resp.Body).Decode(&dec)
	resp.Body.Close()
	ru, _ = url.Parse(dec["redirect"])
	status, tok := token(url.Values{"grant_type": {"authorization_code"}, "code": {ru.Query().Get("code")}, "client_id": {clientID}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if status != 200 {
		t.Fatalf("token: %d %v", status, tok)
	}
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
	if !strings.Contains(strings.Join(toolNames(t, connect(t, e.ts.URL+"/mcp", access)), ","), "alpha__echo") {
		t.Fatal("OAuth access token didn't expose tools")
	}
	// Refresh rotates; the old refresh token is dead.
	status, tok2 := token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	if status != 200 || tok2["access_token"] == access {
		t.Fatalf("refresh: %d %v", status, tok2)
	}
	if c, _ := token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}); c != 400 {
		t.Fatalf("refresh token reused: %d", c)
	}
	// Disabling the user kills access immediately.
	d := true
	e.st.UpdateUser(ctx, e.user.ID, nil, nil, &d)
	req2, _ := http.NewRequest("POST", e.ts.URL+"/mcp", strings.NewReader(`{}`))
	req2.Header.Set("Authorization", "Bearer "+tok2["access_token"].(string))
	resp, _ = http.DefaultClient.Do(req2)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("disabled user still authorised: %d", resp.StatusCode)
	}
}

func TestSignInRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.srv.resolveUser(ctx, "stranger@example.com", "S", ""); err == nil {
		t.Fatal("uninvited user admitted")
	}
	e.st.SetSetting(ctx, settingOpenSignup, "true")
	u, err := e.srv.resolveUser(ctx, "stranger@example.com", "S", "")
	if err != nil || u.IsAdmin {
		t.Fatalf("open signup: %v %+v", err, u)
	}
	d := true
	e.st.UpdateUser(ctx, u.ID, nil, nil, &d)
	if _, err := e.srv.resolveUser(ctx, "stranger@example.com", "S", ""); err == nil {
		t.Fatal("disabled user admitted")
	}
}

func TestPublicMCPsAppearInAuthedEndpoint(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	other, _ := e.st.CreateUser(ctx, "other@example.com", "", false)
	up := mockUpstream(t, "echo")
	mine := e.addUpstream(t, "exa", up.URL, false)
	// Another user shares one MCP publicly and keeps one private. Their public
	// one has the same short name as mine, so it must get a suffix, not shadow mine.
	pub := &store.Upstream{UserID: other.ID, Slug: "exa", Name: "exa", URL: up.URL, Transport: "http", AuthType: "none", Enabled: true, IsPublic: true}
	priv := &store.Upstream{UserID: other.ID, Slug: "secret", Name: "secret", URL: up.URL, Transport: "http", AuthType: "none", Enabled: true}
	for _, u := range []*store.Upstream{pub, priv} {
		if err := e.st.CreateUpstream(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	tok := "mcpt_" + crypto.RandToken(16)
	e.st.CreateAPIToken(ctx, &store.APIToken{UserID: e.user.ID, Name: "t", Prefix: tok[:10]}, tok)
	names := strings.Join(toolNames(t, connect(t, e.ts.URL+"/mcp", tok)), ",")
	if !strings.Contains(names, "exa__echo") || !strings.Contains(names, "exa-"+pub.ID[:4]+"__echo") {
		t.Fatalf("expected own and suffixed public tool, got %s", names)
	}
	if strings.Contains(names, "secret__") {
		t.Fatalf("another user's private MCP leaked: %s", names)
	}
	// A token limited to one MCP gets no public extras.
	scoped := "mcpt_" + crypto.RandToken(16)
	e.st.CreateAPIToken(ctx, &store.APIToken{UserID: e.user.ID, Name: "s", Prefix: scoped[:10], UpstreamIDs: []string{mine.ID}}, scoped)
	if got := toolNames(t, connect(t, e.ts.URL+"/mcp", scoped)); len(got) != 1 {
		t.Fatalf("scoped token saw %v", got)
	}
}

func TestToolsCarryFriendlyLabel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	up := mockUpstream(t, "batch_edit")
	withLabel := &store.Upstream{UserID: e.user.ID, Slug: "link-shortener", Name: "Link Shortener", Label: "shortener", URL: up.URL, Transport: "http", AuthType: "none", Enabled: true}
	noLabel := &store.Upstream{UserID: e.user.ID, Slug: "rc", Name: "Remote Control", URL: up.URL, Transport: "http", AuthType: "none", Enabled: true}
	for _, u := range []*store.Upstream{withLabel, noLabel} {
		if err := e.st.CreateUpstream(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	tok := "mcpt_" + crypto.RandToken(16)
	e.st.CreateAPIToken(ctx, &store.APIToken{UserID: e.user.ID, Name: "t", Prefix: tok[:10]}, tok)
	got := map[string]*mcp.Tool{}
	for tool, err := range connect(t, e.ts.URL+"/mcp", tok).Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		got[tool.Name] = tool
	}
	if tl := got["link-shortener__batch_edit"]; tl == nil || tl.Title != "(shortener) Batch Edit" || tl.Description != "(shortener) echo" {
		t.Fatalf("labelled tool = %+v", tl)
	}
	if tl := got["rc__batch_edit"]; tl == nil || tl.Title != "(rc) Batch Edit" {
		t.Fatalf("slug-labelled tool = %+v", tl)
	}
}
