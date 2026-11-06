package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/store"
)

// reservedSlug is the tool prefix of the gateway's own tools (agora__add_mcp,
// agora__stats), so no MCP can be added under it.
const reservedSlug = "agora"

// addAgoraTools registers the gateway's own management tools. Anonymous callers
// get none. A token limited to specific MCPs gets stats only, because adding an
// MCP would step outside the scope it was limited to.
func (s *Server) addAgoraTools(srv *mcp.Server, p *principal, used map[string]bool) {
	if p.user == nil {
		return
	}
	used[reservedSlug+"__stats"] = true
	mcp.AddTool(srv, &mcp.Tool{
		Name:  reservedSlug + "__stats",
		Title: "Stats (Agora)",
		Description: "(Agora) Show how this gateway is doing: every MCP you can use with its health, " +
			"tool/prompt/resource counts and an estimate of how many tokens its tool definitions cost a client.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ statsIn) (*mcp.CallToolResult, statsOut, error) {
		return nil, s.stats(detached(ctx), p), nil
	})

	if p.scope != nil {
		return
	}
	used[reservedSlug+"__add_mcp"] = true
	mcp.AddTool(srv, &mcp.Tool{
		Name:  reservedSlug + "__add_mcp",
		Title: "Add MCP (Agora)",
		Description: "(Agora) Add a remote MCP server to your gateway. Its tools show up as <short_name>__<tool> the next time the client refreshes its tool list. " +
			"For auth_type \"header\" pass the key in secret; for \"oauth\" the user must press Connect on the MCPs page of the website afterwards.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addMCPIn) (*mcp.CallToolResult, addMCPOut, error) {
		out, err := s.addMCP(detached(ctx), p, in)
		if err != nil {
			return errResult(err.Error()), addMCPOut{}, nil
		}
		return nil, out, nil
	})
}

// detached gives upstream calls made from inside a tool handler a fresh
// context. The handler's own context carries the calling client's negotiated
// protocol version, which the SDK would otherwise send to the upstream server.
func detached(parent context.Context) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	stop := context.AfterFunc(parent, cancel)
	go func() { <-ctx.Done(); stop() }()
	return ctx
}

// ---- agora__add_mcp ----

type addMCPIn struct {
	Name       string `json:"name" jsonschema:"display name, e.g. Exa Search"`
	URL        string `json:"url" jsonschema:"the MCP server's full URL, starting with https://"`
	Slug       string `json:"slug,omitempty" jsonschema:"short name used as tool prefix (a-z, 0-9, dashes); derived from name if empty"`
	Label      string `json:"label,omitempty" jsonschema:"short tag (max 16 chars) shown next to each of its tools"`
	Transport  string `json:"transport,omitempty" jsonschema:"http (default) or sse"`
	AuthType   string `json:"auth_type,omitempty" jsonschema:"none (default), header or oauth"`
	HeaderName string `json:"header_name,omitempty" jsonschema:"header to send the secret in when auth_type is header; default Authorization"`
	Secret     string `json:"secret,omitempty" jsonschema:"API key or bearer token when auth_type is header"`
	IsPublic   bool   `json:"is_public,omitempty" jsonschema:"make it visible on the public endpoint and to other users; default false"`
}

type addMCPOut struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Status    string `json:"status" jsonschema:"connected, needs_auth or unreachable"`
	Tools     int    `json:"tools"`
	Prompts   int    `json:"prompts"`
	Resources int    `json:"resources"`
	Error     string `json:"error,omitempty"`
	Next      string `json:"next,omitempty"`
}

func (s *Server) addMCP(ctx context.Context, p *principal, in addMCPIn) (addMCPOut, error) {
	ui := upstreamInput{Name: &in.Name, URL: &in.URL, IsPublic: &in.IsPublic}
	for _, f := range []struct {
		dst **string
		val string
	}{{&ui.Slug, in.Slug}, {&ui.Label, in.Label}, {&ui.Transport, in.Transport}, {&ui.AuthType, in.AuthType}, {&ui.HeaderName, in.HeaderName}, {&ui.Secret, in.Secret}} {
		if f.val != "" {
			v := f.val
			*f.dst = &v
		}
	}
	u := &store.Upstream{UserID: p.user.ID, Transport: "http", AuthType: "none", Enabled: true}
	if msg := apply(u, &ui); msg != "" {
		return addMCPOut{}, errors.New(msg)
	}
	if err := s.st.CreateUpstream(ctx, u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return addMCPOut{}, errors.New("You already have an MCP with that short name.")
		}
		return addMCPOut{}, errors.New("couldn't save the MCP")
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st := s.up.Check(cctx, u)
	out := addMCPOut{ID: u.ID, Slug: u.Slug, Tools: st.Tools, Prompts: st.Prompts, Resources: st.Resources, Error: st.Error}
	switch {
	case st.OK:
		out.Status = "connected"
		out.Next = "Refresh the tool list in your client to see " + u.Slug + "__* tools."
	case st.NeedsAuth || u.AuthType == "oauth":
		out.Status = "needs_auth"
		out.Next = "Open " + s.cfg.BaseURL + "/mcps and press Connect on this MCP to log in."
	default:
		out.Status = "unreachable"
		out.Next = "The MCP was saved but couldn't be reached. Check the URL and credentials on " + s.cfg.BaseURL + "/mcps."
	}
	return out, nil
}

// ---- agora__stats ----

type statsIn struct{}

type mcpStat struct {
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Mine         bool     `json:"mine" jsonschema:"false for another user's public MCP"`
	Enabled      bool     `json:"enabled"`
	Public       bool     `json:"public"`
	Status       string   `json:"status" jsonschema:"ok, needs_auth, unreachable or disabled"`
	Error        string   `json:"error,omitempty"`
	Tools        int      `json:"tools"`
	Prompts      int      `json:"prompts"`
	Resources    int      `json:"resources"`
	ToolTokens   int      `json:"estimated_tool_tokens" jsonschema:"rough size of this MCP's tool definitions in tokens"`
	LargestTools []string `json:"largest_tools,omitempty"`
}

type statsOut struct {
	MCPs       int       `json:"mcps"`
	Healthy    int       `json:"healthy"`
	Tools      int       `json:"tools"`
	Prompts    int       `json:"prompts"`
	Resources  int       `json:"resources"`
	ToolTokens int       `json:"estimated_tool_tokens" jsonschema:"rough tokens a client spends just loading every tool definition"`
	APITokens  int       `json:"api_tokens"`
	AppGrants  int       `json:"signed_in_apps"`
	PerMCP     []mcpStat `json:"per_mcp"`
}

// estimateTokens is a cheap size guess: about four bytes of JSON per token.
func estimateTokens(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return (len(b) + 3) / 4
}

func (s *Server) stats(ctx context.Context, p *principal) statsOut {
	var ups []*store.Upstream
	own, _ := s.st.ListUpstreams(ctx, p.user.ID)
	for _, u := range own {
		if p.scope == nil || containsID(p.scope, u.ID) {
			ups = append(ups, u)
		}
	}
	if p.scope == nil {
		pub, _ := s.st.ListPublicUpstreams(ctx)
		for _, u := range pub {
			if u.UserID != p.user.ID {
				ups = append(ups, u)
			}
		}
	}
	var live []*store.Upstream
	for _, u := range ups {
		if u.Enabled {
			live = append(live, u)
		}
	}
	snapByID := map[string]*upstreamSnap{}
	for _, us := range s.fetchSnaps(ctx, live) {
		if us != nil {
			snapByID[us.up.ID] = us
		}
	}

	out := statsOut{PerMCP: []mcpStat{}}
	for _, u := range ups {
		ms := mcpStat{Slug: u.Slug, Name: u.Name, Mine: u.UserID == p.user.ID, Enabled: u.Enabled, Public: u.IsPublic}
		switch us := snapByID[u.ID]; {
		case !u.Enabled:
			ms.Status = "disabled"
		case us != nil:
			ms.Status = "ok"
			ms.Tools, ms.Prompts, ms.Resources = len(us.snap.Tools), len(us.snap.Prompts), len(us.snap.Resources)
			type sized struct {
				name string
				n    int
			}
			var sizes []sized
			for _, t := range us.snap.Tools {
				n := estimateTokens(t)
				ms.ToolTokens += n
				sizes = append(sizes, sized{t.Name, n})
			}
			sort.Slice(sizes, func(i, j int) bool { return sizes[i].n > sizes[j].n })
			for i := 0; i < len(sizes) && i < 3; i++ {
				ms.LargestTools = append(ms.LargestTools, fmt.Sprintf("%s (~%d tokens)", sizes[i].name, sizes[i].n))
			}
		default:
			ms.Status = "unreachable"
			if st, ok := s.up.GetStatus(u.ID); ok {
				ms.Error = st.Error
				if st.NeedsAuth {
					ms.Status = "needs_auth"
				}
			}
		}
		if ms.Mine && ms.Error != "" {
			ms.Error = strings.TrimSpace(ms.Error)
		} else if !ms.Mine {
			ms.Error = "" // don't leak details about someone else's server
		}
		out.MCPs++
		if ms.Status == "ok" {
			out.Healthy++
		}
		out.Tools += ms.Tools
		out.Prompts += ms.Prompts
		out.Resources += ms.Resources
		out.ToolTokens += ms.ToolTokens
		out.PerMCP = append(out.PerMCP, ms)
	}
	sort.Slice(out.PerMCP, func(i, j int) bool { return out.PerMCP[i].ToolTokens > out.PerMCP[j].ToolTokens })
	if ts, err := s.st.ListAPITokens(ctx, p.user.ID); err == nil {
		out.APITokens = len(ts)
	}
	if gs, err := s.st.ListGrants(ctx, p.user.ID); err == nil {
		out.AppGrants = len(gs)
	}
	return out
}

func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
