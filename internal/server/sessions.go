package server

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// sessionIdleTimeout closes MCP sessions that have been quiet this long. Clients
// that come back with the old session id get a 404 and re-initialize.
const sessionIdleTimeout = 2 * time.Hour

// sessionEntry is one client session's own MCP server plus what it exposes now.
type sessionEntry struct {
	srv     *mcp.Server
	p       *principal
	key     string
	created time.Time

	mu  sync.Mutex // serialises refreshes
	cat catalog
}

// sessionRegistry tracks live gateway sessions so catalogue changes can be
// pushed to them as list_changed notifications.
type sessionRegistry struct {
	mu      sync.Mutex
	entries map[*sessionEntry]struct{}
}

// ownerKey identifies who a session belongs to, including token scope.
func (p *principal) ownerKey() string {
	if p.user == nil {
		return "anon"
	}
	k := "u:" + p.user.ID
	if p.scope != nil {
		sc := slices.Clone(p.scope)
		slices.Sort(sc)
		k += ":" + strings.Join(sc, ",")
	}
	return k
}

func (r *sessionRegistry) add(e *sessionEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = map[*sessionEntry]struct{}{}
	}
	// Drop servers whose sessions have all ended (a new entry gets a grace
	// period because its session isn't connected until after getServer returns).
	for o := range r.entries {
		if time.Since(o.created) > time.Minute && !hasSession(o.srv) {
			delete(r.entries, o)
		}
	}
	r.entries[e] = struct{}{}
}

func hasSession(srv *mcp.Server) bool {
	for range srv.Sessions() {
		return true
	}
	return false
}

func (r *sessionRegistry) snapshot() []*sessionEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*sessionEntry, 0, len(r.entries))
	for e := range r.entries {
		out = append(out, e)
	}
	return out
}

func (r *sessionRegistry) find(id string) *sessionEntry {
	for _, e := range r.snapshot() {
		for ss := range e.srv.Sessions() {
			if ss.ID() == id {
				return e
			}
		}
	}
	return nil
}

// owns reports whether the caller p is the one who opened session id.
func (r *sessionRegistry) owns(id string, p *principal) bool {
	e := r.find(id)
	return e == nil || e.key == p.ownerKey()
}

// newSession builds the server for a new client session and starts tracking it.
func (s *Server) newSession(ctx context.Context, p *principal) *mcp.Server {
	srv, cat := s.buildServer(ctx, p)
	s.sessions.add(&sessionEntry{srv: srv, p: p, key: p.ownerKey(), created: time.Now(), cat: cat})
	return srv
}

// upstreamsChanged tells connected clients that the set of tools may have
// changed because an MCP owned by ownerID was added, removed, enabled,
// disabled or edited. Pass public=true if it is (or was) shared with everyone.
// An empty ownerID refreshes every session. It returns immediately.
func (s *Server) upstreamsChanged(ownerID string, public bool) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, e := range s.sessions.snapshot() {
			if ownerID != "" && !public && (e.p.user == nil || e.p.user.ID != ownerID) {
				continue
			}
			s.refresh(ctx, e)
		}
	}()
}

// refresh re-reads what the session's caller may see, registers it, and drops
// what disappeared. The SDK sends list_changed to the client for each change.
func (s *Server) refresh(ctx context.Context, e *sessionEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !hasSession(e.srv) {
		return
	}
	old := e.cat
	cat := s.fill(ctx, e.srv, e.p)
	diff := func(before, after []string) []string {
		var gone []string
		for _, n := range before {
			if !slices.Contains(after, n) {
				gone = append(gone, n)
			}
		}
		return gone
	}
	if g := diff(old.tools, cat.tools); len(g) > 0 {
		e.srv.RemoveTools(g...)
	}
	if g := diff(old.prompts, cat.prompts); len(g) > 0 {
		e.srv.RemovePrompts(g...)
	}
	if g := diff(old.resources, cat.resources); len(g) > 0 {
		e.srv.RemoveResources(g...)
	}
	e.cat = cat
}
