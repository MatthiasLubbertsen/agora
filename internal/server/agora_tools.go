package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/store"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/upstream"
)

// reservedSlug is the tool prefix of the gateway's own tools (agora__add_mcp,
// agora__stats), so no MCP can be added under it.
const reservedSlug = "agora"

// agoraInstructions is the server-level `instructions` text clients show their
// model. It describes the merged namespace and the gateway's own tools.
func agoraInstructions(p *principal) string {
	b := "Agora is one MCP endpoint that merges several MCP servers. Every tool and prompt name starts with the short name of the MCP it comes from, followed by two underscores (for example exa__search)."
	if p.user == nil {
		return b
	}
	b += "\n\nAgora's own tools use the prefix agora__. Call agora__info for an overview of the idea."
	b += "\n\nagora__stats lists every MCP with its health and counts. An MCP whose status is needs_auth has lost its login (or never finished it); " +
		"stats then includes a login_url for it. Give that link to the user, tell them to open it in a browser where they are signed in to Agora, " +
		"and ask them to tell you once they have approved so you can try again. The same applies when a tool call fails with a message saying the MCP isn't logged in: " +
		"the message contains a fresh login link. Links work once and expire after 10 minutes, so call agora__stats again if the user needs a new one."
	if p.scope == nil {
		b += "\n\nagora__add_mcp adds a new remote MCP. If the result has a login_url, give it to the user the same way."
	}
	return b
}

// addAgoraTools registers the gateway's own management tools. Anonymous callers
// get none. A token limited to specific MCPs gets stats only, because adding an
// MCP would step outside the scope it was limited to.
func (s *Server) addAgoraTools(srv *mcp.Server, p *principal, used map[string]bool) {
	if p.user == nil {
		return
	}
	used[reservedSlug+"__info"] = true
	mcp.AddTool(srv, &mcp.Tool{
		Name:  reservedSlug + "__info",
		Title: "Info (Agora)",
		Description: "(Agora) Explain what Agora is, how its tools are named, what the agora__ tools do and how logging in to an MCP works. " +
			"Call this when the user asks what Agora is or how to use it.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ infoIn) (*mcp.CallToolResult, infoOut, error) {
		return nil, s.info(p), nil
	})

	used[reservedSlug+"__stats"] = true
	mcp.AddTool(srv, &mcp.Tool{
		Name:  reservedSlug + "__stats",
		Title: "Stats (Agora)",
		Description: "(Agora) Show how this gateway is doing: every MCP you can use with its health, " +
			"tool/prompt/resource counts and a login link for any of your MCPs that need one.",
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
			"For auth_type \"header\" pass the key in secret. If the MCP needs a login (auth_type \"oauth\", or detected automatically), the result has a login_url: give it to the user to open in their browser.",
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

// loginLink starts the upstream OAuth flow and returns the URL the user must open.
func (s *Server) loginLink(ctx context.Context, u *store.Upstream) (string, error) {
	return s.up.StartOAuth(detached(ctx), u, s.cfg.BaseURL+"/oauth/upstream/callback", "", "")
}

// reauthMessage turns an "unauthenticated" failure of one of the caller's own
// OAuth MCPs into an instruction for the agent, carrying a fresh login link.
// It returns "" for any other failure.
func (s *Server) reauthMessage(ctx context.Context, p *principal, u *store.Upstream, callErr error) string {
	if p.user == nil || u.UserID != p.user.ID || u.AuthType != "oauth" || !upstream.IsAuthErr(callErr) {
		return ""
	}
	link, err := s.loginLink(ctx, u)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s isn't logged in (the login expired or was never completed), so this call did not run. "+
		"Give this link to the user so they can log in again: %s\n"+
		"The link works once and expires in 10 minutes; they need to be signed in to Agora in that browser. "+
		"Ask them to tell you when they're done, then try this call again.", u.Name, link)
}

// ---- agora__info ----

type infoIn struct{}

type infoOut struct {
	Overview string   `json:"overview"`
	Naming   string   `json:"naming"`
	Tools    []string `json:"agora_tools"`
	Login    string   `json:"logging_in"`
	Website  string   `json:"website"`
}

func (s *Server) info(p *principal) infoOut {
	out := infoOut{
		Overview: "Agora is an MCP gateway: one MCP endpoint that fronts many MCP servers. The user adds remote MCPs once (on the website or with agora__add_mcp) " +
			"and every client they use (Claude, Cursor, VS Code, ...) connects to this single URL and sees all of them. Each user's MCPs, tokens and logins are private to them, " +
			"except MCPs they explicitly mark public.",
		Naming: "Every tool and prompt is named <short_name>__<tool>, where short_name is the MCP's slug, e.g. exa__search. Titles and descriptions are tagged with the MCP's label, e.g. \"Search (Exa)\".",
		Tools: []string{
			"agora__info: this overview.",
			"agora__stats: health, tool/prompt/resource counts for each MCP, and login links for MCPs that need one.",
		},
		Login: "Some MCPs need the user to log in. When agora__stats shows status needs_auth, or a tool call says the MCP isn't logged in, there is a login_url. " +
			"Give it to the user, have them open it in a browser where they are signed in to Agora, and ask them to tell you when they're done; then try again. " +
			"A link works once and lasts 10 minutes.",
		Website: s.cfg.BaseURL,
	}
	if p.scope == nil {
		out.Tools = append(out.Tools, "agora__add_mcp: add a remote MCP by URL; it is tested right away and a login_url is returned if it needs a login.")
	}
	return out
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
	LoginURL  string `json:"login_url,omitempty" jsonschema:"when status is needs_auth: a link the user must open in their browser to log in to this MCP; valid for 10 minutes"`
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
	defer s.upstreamsChanged(u.UserID, u.IsPublic) // connected clients pick the new tools up without reconnecting
	out := addMCPOut{ID: u.ID, Slug: u.Slug, Tools: st.Tools, Prompts: st.Prompts, Resources: st.Resources, Error: st.Error}
	// A server that wants a login but was added without auth is switched to
	// OAuth, so the user gets a login link instead of a dead end.
	if !st.OK && st.NeedsAuth && u.AuthType == "none" && in.AuthType == "" {
		u.AuthType = "oauth"
		if err := s.st.UpdateUpstream(ctx, u); err == nil {
			s.up.Forget(u.ID)
		} else {
			u.AuthType = "none"
		}
	}
	switch {
	case st.OK:
		out.Status = "connected"
		out.Next = "Refresh the tool list in your client to see " + u.Slug + "__* tools."
	case u.AuthType == "oauth":
		out.Status, out.Error = "needs_auth", ""
		link, err := s.loginLink(ctx, u)
		if err != nil {
			out.Error = err.Error()
			out.Next = "The MCP was saved but automatic login setup failed. The user can finish on " + s.cfg.BaseURL + "/mcps (press Connect, entering a client ID and secret if asked)."
			break
		}
		out.LoginURL = link
		out.Next = "Tell the user to log in to " + u.Name + " by opening login_url in a browser where they are signed in to Agora as the same account. " +
			"The link works once and expires in 10 minutes. Afterwards refresh the tool list to see " + u.Slug + "__* tools."
	case st.NeedsAuth:
		out.Status = "needs_auth"
		out.Next = "The MCP rejected the credentials. Check the key on " + s.cfg.BaseURL + "/mcps."
	default:
		out.Status = "unreachable"
		out.Next = "The MCP was saved but couldn't be reached. Check the URL and credentials on " + s.cfg.BaseURL + "/mcps."
	}
	return out, nil
}

// ---- agora__stats ----

type statsIn struct{}

type mcpStat struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Mine      bool   `json:"mine" jsonschema:"false for another user's public MCP"`
	Enabled   bool   `json:"enabled"`
	Public    bool   `json:"public"`
	Status    string `json:"status" jsonschema:"ok, needs_auth, unreachable or disabled"`
	Error     string `json:"error,omitempty"`
	LoginURL  string `json:"login_url,omitempty" jsonschema:"for your own MCP with status needs_auth: give this link to the user so they can log in again"`
	Tools     int    `json:"tools"`
	Prompts   int    `json:"prompts"`
	Resources int    `json:"resources"`
}

type statsOut struct {
	MCPs      int       `json:"mcps"`
	Healthy   int       `json:"healthy"`
	Tools     int       `json:"tools"`
	Prompts   int       `json:"prompts"`
	Resources int       `json:"resources"`
	APITokens int       `json:"api_tokens"`
	AppGrants int       `json:"signed_in_apps"`
	PerMCP    []mcpStat `json:"per_mcp"`
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
		default:
			ms.Status = "unreachable"
			if st, ok := s.up.GetStatus(u.ID); ok {
				ms.Error = st.Error
				if st.NeedsAuth {
					ms.Status = "needs_auth"
				}
			}
		}
		if ms.Status == "needs_auth" && ms.Mine && u.AuthType == "oauth" {
			if link, err := s.loginLink(ctx, u); err == nil {
				ms.LoginURL = link
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
		out.PerMCP = append(out.PerMCP, ms)
	}
	sort.Slice(out.PerMCP, func(i, j int) bool { return out.PerMCP[i].Slug < out.PerMCP[j].Slug })
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
