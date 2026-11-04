package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/store"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/upstream"
)

type principal struct {
	user  *store.User // nil for anonymous
	scope []string    // nil = all of the user's MCPs
}

type principalKey struct{}

func (s *Server) resourceMetadataURL() string {
	return s.cfg.BaseURL + "/.well-known/oauth-protected-resource/mcp"
}

func (s *Server) unauthorized(w http.ResponseWriter, desc string) {
	h := `Bearer resource_metadata="` + s.resourceMetadataURL() + `"`
	if desc != "" {
		h += `, error="invalid_token", error_description="` + desc + `"`
	}
	w.Header().Set("WWW-Authenticate", h)
	writeErr(w, 401, "authentication required")
}

func corsMCP(w http.ResponseWriter, r *http.Request) bool {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID")
	h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	h.Set("Access-Control-Expose-Headers", "Mcp-Session-Id, WWW-Authenticate")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

// gatewayHandler serves the merged MCP endpoint. The authenticated endpoint
// shows a user's own MCPs; the public one (when enabled by an admin) shows
// only MCPs their owners ticked "public".
func (s *Server) gatewayHandler(public bool) http.Handler {
	mcpH := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, _ := r.Context().Value(principalKey{}).(*principal)
		if p == nil {
			return nil
		}
		return s.buildServer(r.Context(), p)
	}, &mcp.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if corsMCP(w, r) {
			return
		}
		var p *principal
		if public {
			if !s.st.GetBool(r.Context(), settingAllowAnonymous) {
				writeErr(w, 404, "the public endpoint is turned off")
				return
			}
			p = &principal{}
		} else {
			var ok bool
			p, ok = s.authenticateBearer(w, r)
			if !ok {
				return
			}
		}
		mcpH.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func (s *Server) authenticateBearer(w http.ResponseWriter, r *http.Request) (*principal, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		s.unauthorized(w, "")
		return nil, false
	}
	tok := strings.TrimSpace(h[7:])
	switch {
	case strings.HasPrefix(tok, "mcpt_"):
		t, u, err := s.st.LookupAPIToken(r.Context(), tok)
		if err != nil {
			s.unauthorized(w, "invalid or expired token")
			return nil, false
		}
		return &principal{user: u, scope: t.UpstreamIDs}, true
	case strings.HasPrefix(tok, "mcpo_"):
		u, err := s.st.LookupOAuthAccess(r.Context(), tok)
		if err != nil {
			s.unauthorized(w, "invalid or expired token")
			return nil, false
		}
		return &principal{user: u}, true
	}
	s.unauthorized(w, "unrecognised token")
	return nil, false
}

// principalUpstreams lists the upstreams a caller may see: their own, plus
// every enabled upstream that any user marked Public. A token limited to
// specific MCPs sees exactly those and nothing else.
func (s *Server) principalUpstreams(ctx context.Context, p *principal) []*store.Upstream {
	if p.user == nil {
		ups, _ := s.st.ListPublicUpstreams(ctx)
		return ups
	}
	var ups []*store.Upstream
	all, _ := s.st.ListUpstreams(ctx, p.user.ID)
	for _, u := range all {
		if !u.Enabled {
			continue
		}
		if p.scope != nil && !slices.Contains(p.scope, u.ID) {
			continue
		}
		ups = append(ups, u)
	}
	if p.scope == nil {
		pub, _ := s.st.ListPublicUpstreams(ctx)
		for _, u := range pub {
			if u.UserID != p.user.ID {
				ups = append(ups, u)
			}
		}
	}
	return ups
}

func objectSchema(in any) any {
	m, ok := in.(map[string]any)
	if !ok || m == nil {
		return map[string]any{"type": "object"}
	}
	if t, _ := m["type"].(string); t != "object" {
		cp := make(map[string]any, len(m)+1)
		for k, v := range m {
			cp[k] = v
		}
		cp["type"] = "object"
		return cp
	}
	return m
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// buildServer assembles an MCP server exposing every tool, prompt and resource
// of the caller's upstreams, each prefixed with the upstream's short name.
func (s *Server) buildServer(ctx context.Context, p *principal) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "mofm", Version: "1.0.0"}, &mcp.ServerOptions{
		Instructions: "This server merges several MCP servers. Every tool and prompt name starts with the name of the MCP it comes from, followed by two underscores.",
		// What a caller sees depends on who they are, so no shared cache may keep it.
		SetCacheable: func(_ context.Context, _ mcp.Request, c *mcp.Cacheable) { c.TTLMs, c.CacheScope = 0, "private" },
	})
	ups := s.principalUpstreams(ctx, p)

	// Fetch every upstream's catalogue in parallel, bounded so one slow server
	// can't hold up the rest.
	fctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	snaps := make([]*upstreamSnap, len(ups))
	var wg sync.WaitGroup
	for i, u := range ups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap, err := s.up.Snapshot(fctx, u)
			if snap == nil {
				if err != nil {
					s.log.Warn("upstream unavailable", "slug", u.Slug, "err", err)
				}
				return
			}
			snaps[i] = &upstreamSnap{up: u, snap: snap}
		}()
	}
	wg.Wait()

	usedSlug := map[string]bool{}
	usedName := map[string]bool{}
	for _, us := range snaps {
		if us == nil {
			continue
		}
		slug := us.up.Slug
		if usedSlug[slug] { // someone else's public MCP with the same short name: stable suffix
			slug = slug + "-" + us.up.ID[:4]
		}
		usedSlug[slug] = true
		s.addUpstream(srv, slug, us, usedName)
	}
	return srv
}

type upstreamSnap struct {
	up   *store.Upstream
	snap *upstream.Snapshot
}

// humanize turns "batch_edit" into "Batch Edit" for tools that have no title.
func humanize(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' || r == ' ' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// tagged puts the upstream's label in front, so "List" becomes "(Exa) List".
func tagged(label, text string) string {
	if text == "" {
		return "(" + label + ")"
	}
	return "(" + label + ") " + text
}

func gwName(slug, name string) string {
	n := slug + "__" + name
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

func (s *Server) addUpstream(srv *mcp.Server, slug string, us *upstreamSnap, used map[string]bool) {
	up := us.up
	label := up.DisplayLabel()
	for _, t := range us.snap.Tools {
		t := t
		name := gwName(slug, t.Name)
		if used[name] {
			continue
		}
		used[name] = true
		title := t.Title
		if title == "" {
			title = humanize(t.Name)
		}
		tool := &mcp.Tool{Name: name, Title: tagged(label, title), Description: tagged(label, t.Description), Annotations: t.Annotations,
			InputSchema: objectSchema(t.InputSchema), OutputSchema: t.OutputSchema}
		func() {
			defer func() { _ = recover() }() // a malformed upstream schema must not break the whole server
			srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				var args any
				if len(req.Params.Arguments) > 0 {
					args = req.Params.Arguments
				}
				var res *mcp.CallToolResult
				err := s.up.With(ctx, up, func(cs *mcp.ClientSession) error {
					var e error
					res, e = cs.CallTool(ctx, &mcp.CallToolParams{Name: t.Name, Arguments: args})
					return e
				})
				if err != nil {
					return errResult(fmt.Sprintf("%s: %v", up.Name, err)), nil
				}
				return res, nil
			})
		}()
	}
	for _, pr := range us.snap.Prompts {
		pr := pr
		name := gwName(slug, pr.Name)
		if used["prompt:"+name] {
			continue
		}
		used["prompt:"+name] = true
		ptitle := pr.Title
		if ptitle == "" {
			ptitle = humanize(pr.Name)
		}
		srv.AddPrompt(&mcp.Prompt{Name: name, Title: tagged(label, ptitle), Description: tagged(label, pr.Description), Arguments: pr.Arguments},
			func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				var res *mcp.GetPromptResult
				err := s.up.With(ctx, up, func(cs *mcp.ClientSession) error {
					var e error
					res, e = cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: pr.Name, Arguments: req.Params.Arguments})
					return e
				})
				return res, err
			})
	}
	for _, rs := range us.snap.Resources {
		rs := rs
		gw := "mcpgw://" + slug + "/" + url.PathEscape(rs.URI)
		if used[gw] {
			continue
		}
		used[gw] = true
		srv.AddResource(&mcp.Resource{URI: gw, Name: label + ": " + rs.Name, Title: rs.Title, Description: rs.Description,
			MIMEType: rs.MIMEType, Size: rs.Size, Annotations: rs.Annotations},
			func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				var res *mcp.ReadResourceResult
				err := s.up.With(ctx, up, func(cs *mcp.ClientSession) error {
					var e error
					res, e = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: rs.URI})
					return e
				})
				if res != nil {
					for _, c := range res.Contents {
						c.URI = gw
					}
				}
				return res, err
			})
	}
}
