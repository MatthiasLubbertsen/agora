package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/store"
)

const snapshotTTL = 60 * time.Second

// Snapshot is what an upstream advertised the last time we asked.
type Snapshot struct {
	Tools     []*mcp.Tool
	Prompts   []*mcp.Prompt
	Resources []*mcp.Resource
	Fetched   time.Time
}

// Status is the health of an upstream as last observed.
type Status struct {
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	NeedsAuth bool      `json:"needs_auth"`
	Tools     int       `json:"tools"`
	Prompts   int       `json:"prompts"`
	Resources int       `json:"resources"`
	CheckedAt time.Time `json:"checked_at"`
}

type entry struct {
	mu          sync.Mutex
	sess        *mcp.ClientSession
	fingerprint string
	snap        *Snapshot
	status      Status
	tsMu        sync.Mutex
	tokenSrc    oauth2.TokenSource
}

// Manager owns the client connections to every upstream MCP server.
type Manager struct {
	store *store.Store
	httpc *http.Client

	mu      sync.Mutex
	entries map[string]*entry
}

func NewManager(st *store.Store, allowPrivate bool) *Manager {
	return &Manager{store: st, httpc: NewHTTPClient(allowPrivate), entries: map[string]*entry{}}
}

// HTTPClient is the guarded client used for all outbound upstream traffic.
func (m *Manager) HTTPClient() *http.Client { return m.httpc }

func (m *Manager) entry(id string) *entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[id]
	if e == nil {
		e = &entry{}
		m.entries[id] = e
	}
	return e
}

// Forget closes and drops the cached connection (after an edit or delete).
func (m *Manager) Forget(id string) {
	m.mu.Lock()
	e := m.entries[id]
	delete(m.entries, id)
	m.mu.Unlock()
	if e != nil {
		e.mu.Lock()
		e.closeLocked()
		e.mu.Unlock()
	}
}

func (e *entry) closeLocked() {
	if e.sess != nil {
		e.sess.Close()
		e.sess = nil
	}
	e.tsMu.Lock()
	e.tokenSrc = nil
	e.tsMu.Unlock()
}

// GetStatus returns the last known status without touching the network.
func (m *Manager) GetStatus(id string) (Status, bool) {
	m.mu.Lock()
	e := m.entries[id]
	m.mu.Unlock()
	if e == nil {
		return Status{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status, !e.status.CheckedAt.IsZero()
}

func fingerprint(up *store.Upstream) string {
	return strings.Join([]string{up.URL, up.Transport, up.AuthType, up.HeaderName, up.Secret}, "\x00")
}

// authTransport adds credentials to every outgoing request.
type authTransport struct {
	base http.RoundTripper
	up   store.Upstream
	m    *Manager
	e    *entry
}

func (t *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	switch t.up.AuthType {
	case "header":
		name := t.up.HeaderName
		if name == "" {
			name = "Authorization"
		}
		val := t.up.Secret
		if strings.EqualFold(name, "Authorization") && !strings.Contains(val, " ") {
			val = "Bearer " + val
		}
		r.Header.Set(name, val)
	case "oauth":
		src, err := t.m.tokenSource(r.Context(), &t.up, t.e)
		if err != nil {
			return nil, err
		}
		tok, err := src.Token()
		if err != nil {
			return nil, fmt.Errorf("upstream login expired, reconnect it: %w", err)
		}
		r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	return t.base.RoundTrip(r)
}

type persistSource struct {
	src   oauth2.TokenSource
	save  func(*oauth2.Token)
	mu    sync.Mutex
	lastA string
}

func (p *persistSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if tok.AccessToken != p.lastA {
		p.lastA = tok.AccessToken
		p.save(tok)
	}
	return tok, nil
}

// tokenSource builds (and caches) a refreshing, persisting token source.
// Callers must not hold e.mu, because the transport calls this mid-request.
func (m *Manager) tokenSource(ctx context.Context, up *store.Upstream, e *entry) (oauth2.TokenSource, error) {
	e.tsMu.Lock()
	src := e.tokenSrc
	e.tsMu.Unlock()
	if src != nil {
		return src, nil
	}
	o, err := m.store.GetUpstreamOAuth(ctx, up.ID)
	if err != nil || o.Token == "" {
		return nil, errors.New("not connected: use Connect to log in to this MCP")
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(o.Token), &tok); err != nil {
		return nil, fmt.Errorf("stored token unreadable: %w", err)
	}
	cfg := &oauth2.Config{ClientID: o.ClientID, ClientSecret: o.ClientSecret,
		Endpoint: oauth2.Endpoint{AuthURL: o.AuthURL, TokenURL: o.TokenURL, AuthStyle: oauth2.AuthStyleAutoDetect}}
	hctx := context.WithValue(context.Background(), oauth2.HTTPClient, m.httpc)
	id := up.ID
	src = &persistSource{lastA: tok.AccessToken, src: cfg.TokenSource(hctx, &tok), save: func(t *oauth2.Token) {
		b, _ := json.Marshal(t)
		_ = m.store.SaveUpstreamToken(context.Background(), id, string(b))
	}}
	e.tsMu.Lock()
	e.tokenSrc = src
	e.tsMu.Unlock()
	return src, nil
}

func (m *Manager) connect(ctx context.Context, up *store.Upstream, e *entry) (*mcp.ClientSession, error) {
	hc := &http.Client{Transport: &authTransport{base: m.httpc.Transport, up: *up, m: m, e: e}}
	var tr mcp.Transport
	switch up.Transport {
	case "sse":
		tr = &mcp.SSEClientTransport{Endpoint: up.URL, HTTPClient: hc}
	default:
		tr = &mcp.StreamableClientTransport{Endpoint: up.URL, HTTPClient: hc, MaxRetries: 2, DisableStandaloneSSE: true}
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "mofm", Version: "1.0.0"}, nil)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return cl.Connect(cctx, tr, nil)
}

// session returns a live session, connecting if needed. e.mu must be held.
func (m *Manager) session(ctx context.Context, up *store.Upstream, e *entry) (*mcp.ClientSession, error) {
	fp := fingerprint(up)
	if e.sess != nil && e.fingerprint != fp {
		e.closeLocked()
		e.snap = nil
	}
	if e.sess != nil {
		return e.sess, nil
	}
	s, err := m.connect(ctx, up, e)
	if err != nil {
		return nil, err
	}
	e.sess, e.fingerprint = s, fp
	return s, nil
}

func isAuthErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "401") || strings.Contains(s, "unauthorized") || strings.Contains(s, "not connected") ||
		strings.Contains(s, "login expired")
}

func short(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// refresh re-lists everything from the upstream. e.mu must be held.
func (m *Manager) refresh(ctx context.Context, up *store.Upstream, e *entry) (*Snapshot, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		s, err := m.session(ctx, up, e)
		if err != nil {
			lastErr = err
			break
		}
		lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		snap, err := listAll(lctx, s)
		cancel()
		if err == nil {
			snap.Fetched = time.Now()
			e.snap = snap
			e.status = Status{OK: true, Tools: len(snap.Tools), Prompts: len(snap.Prompts), Resources: len(snap.Resources), CheckedAt: time.Now()}
			return snap, nil
		}
		lastErr = err
		e.closeLocked() // stale connection: drop it and retry once with a fresh one
	}
	e.status = Status{Error: short(lastErr), NeedsAuth: isAuthErr(lastErr), CheckedAt: time.Now()}
	return nil, lastErr
}

func listAll(ctx context.Context, s *mcp.ClientSession) (*Snapshot, error) {
	snap := &Snapshot{}
	caps := s.InitializeResult().Capabilities
	if caps != nil && caps.Tools != nil {
		for t, err := range s.Tools(ctx, nil) {
			if err != nil {
				return nil, err
			}
			snap.Tools = append(snap.Tools, t)
		}
	}
	if caps != nil && caps.Prompts != nil {
		for p, err := range s.Prompts(ctx, nil) {
			if err != nil {
				return nil, err
			}
			snap.Prompts = append(snap.Prompts, p)
		}
	}
	if caps != nil && caps.Resources != nil {
		for r, err := range s.Resources(ctx, nil) {
			if err != nil {
				return nil, err
			}
			snap.Resources = append(snap.Resources, r)
		}
	}
	return snap, nil
}

// Snapshot returns the upstream's catalogue, using a short-lived cache. If a
// refresh fails but an older snapshot exists, the older one is returned along
// with the error.
func (m *Manager) Snapshot(ctx context.Context, up *store.Upstream) (*Snapshot, error) {
	e := m.entry(up.ID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snap != nil && e.fingerprint == fingerprint(up) && time.Since(e.snap.Fetched) < snapshotTTL {
		return e.snap, nil
	}
	snap, err := m.refresh(ctx, up, e)
	if err != nil {
		return e.snap, err
	}
	return snap, nil
}

// Check forces a fresh connection and listing, and reports the result.
func (m *Manager) Check(ctx context.Context, up *store.Upstream) Status {
	e := m.entry(up.ID)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeLocked()
	_, _ = m.refresh(ctx, up, e)
	return e.status
}

// With runs fn on a live session for the upstream, reconnecting once if the
// connection turns out to be dead.
func (m *Manager) With(ctx context.Context, up *store.Upstream, fn func(*mcp.ClientSession) error) error {
	e := m.entry(up.ID)
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		e.mu.Lock()
		var s *mcp.ClientSession
		s, err = m.session(ctx, up, e)
		e.mu.Unlock()
		if err != nil {
			return err
		}
		if err = fn(s); err == nil {
			return nil
		}
		// A JSON-RPC error from the tool itself is not a transport failure.
		if !isTransportErr(err) {
			return err
		}
		e.mu.Lock()
		if e.sess == s {
			e.closeLocked()
		}
		e.mu.Unlock()
	}
	return err
}

func isTransportErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "connection closed") || strings.Contains(s, "eof") || strings.Contains(s, "closed") ||
		strings.Contains(s, "session not found") || strings.Contains(s, "broken pipe") || strings.Contains(s, "reset by peer") ||
		strings.Contains(s, "404")
}
