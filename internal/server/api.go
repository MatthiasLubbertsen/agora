package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/crypto"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/store"
	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/upstream"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ---- upstreams ----

type upstreamView struct {
	*store.Upstream
	HasSecret      bool             `json:"has_secret"`
	OAuthConnected bool             `json:"oauth_connected"`
	Status         *upstream.Status `json:"status"`
}

func (s *Server) view(ctx context.Context, u *store.Upstream) upstreamView {
	v := upstreamView{Upstream: u, HasSecret: u.Secret != ""}
	if u.AuthType == "oauth" {
		v.OAuthConnected = s.up.Connected(ctx, u.ID)
	}
	if st, ok := s.up.GetStatus(u.ID); ok {
		v.Status = &st
	}
	return v
}

func (s *Server) handleUpstreamList(w http.ResponseWriter, r *http.Request) {
	ups, err := s.st.ListUpstreams(r.Context(), userFrom(r).ID)
	if err != nil {
		writeErr(w, 500, "couldn't load MCPs")
		return
	}
	out := make([]upstreamView, 0, len(ups))
	for _, u := range ups {
		out = append(out, s.view(r.Context(), u))
	}
	writeJSON(w, 200, out)
}

type upstreamInput struct {
	Name       *string `json:"name"`
	Slug       *string `json:"slug"`
	Label      *string `json:"label"`
	URL        *string `json:"url"`
	Transport  *string `json:"transport"`
	AuthType   *string `json:"auth_type"`
	HeaderName *string `json:"header_name"`
	Secret     *string `json:"secret"`
	IsPublic   *bool   `json:"is_public"`
	Enabled    *bool   `json:"enabled"`
}

func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 32 {
		out = strings.Trim(out[:32], "-")
	}
	return out
}

// apply copies provided fields onto u and validates the result.
func apply(u *store.Upstream, in *upstreamInput) string {
	if in.Name != nil {
		u.Name = strings.TrimSpace(*in.Name)
	}
	if in.Slug != nil {
		u.Slug = strings.TrimSpace(strings.ToLower(*in.Slug))
	}
	if in.Label != nil {
		u.Label = strings.TrimSpace(*in.Label)
	}
	if in.URL != nil {
		u.URL = strings.TrimSpace(*in.URL)
	}
	if in.Transport != nil {
		u.Transport = *in.Transport
	}
	if in.AuthType != nil {
		u.AuthType = *in.AuthType
	}
	if in.HeaderName != nil {
		u.HeaderName = strings.TrimSpace(*in.HeaderName)
	}
	if in.Secret != nil && *in.Secret != "" {
		u.Secret = strings.TrimSpace(*in.Secret)
	}
	if in.IsPublic != nil {
		u.IsPublic = *in.IsPublic
	}
	if in.Enabled != nil {
		u.Enabled = *in.Enabled
	}
	if u.Name == "" || len(u.Name) > 80 {
		return "Give it a name (up to 80 characters)."
	}
	if u.Slug == "" {
		u.Slug = slugify(u.Name)
	}
	if !slugRe.MatchString(u.Slug) {
		return "The short name can only use a-z, 0-9 and dashes (max 32). It becomes the tool prefix, e.g. " + u.Slug + "__search."
	}
	if u.Slug == reservedSlug {
		return "The short name \"" + reservedSlug + "\" is reserved for the gateway's own tools."
	}
	if utf8.RuneCountInString(u.Label) > 16 || strings.ContainsAny(u.Label, "\r\n\t") {
		return "The label can be up to 16 characters, like Exa or rc."
	}
	pu, err := url.Parse(u.URL)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		return "Enter the MCP server's full URL, starting with https://"
	}
	if pu.User != nil {
		return "Don't put credentials in the URL, use the auth options instead."
	}
	switch u.Transport {
	case "http", "sse":
	default:
		return "Transport must be http or sse."
	}
	switch u.AuthType {
	case "none", "oauth":
		u.HeaderName, u.Secret = "", ""
	case "header":
		if u.Secret == "" {
			return "Enter the token or key to send."
		}
		if u.HeaderName != "" && !regexp.MustCompile(`^[A-Za-z0-9-]+$`).MatchString(u.HeaderName) {
			return "That isn't a valid header name."
		}
	default:
		return "Unknown auth type."
	}
	return ""
}

func (s *Server) handleUpstreamCreate(w http.ResponseWriter, r *http.Request) {
	var in upstreamInput
	if !readJSON(w, r, &in) {
		return
	}
	u := &store.Upstream{UserID: userFrom(r).ID, Transport: "http", AuthType: "none", Enabled: true}
	if msg := apply(u, &in); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.st.CreateUpstream(r.Context(), u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, "You already have an MCP with that short name.")
			return
		}
		writeErr(w, 500, "couldn't save")
		return
	}
	s.upstreamsChanged(u.UserID, u.IsPublic)
	writeJSON(w, 201, s.view(r.Context(), u))
}

func (s *Server) handleUpstreamUpdate(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUpstream(r.Context(), userFrom(r).ID, r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	oldURL, oldAuth, wasPublic := u.URL, u.AuthType, u.IsPublic
	var in upstreamInput
	if !readJSON(w, r, &in) {
		return
	}
	if msg := apply(u, &in); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := s.st.UpdateUpstream(r.Context(), u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, "You already have an MCP with that short name.")
			return
		}
		writeErr(w, 500, "couldn't save")
		return
	}
	if u.URL != oldURL || u.AuthType != oldAuth {
		_ = s.st.DeleteUpstreamOAuth(r.Context(), u.ID) // login was for the old server
	}
	s.up.Forget(u.ID)
	s.upstreamsChanged(u.UserID, u.IsPublic || wasPublic)
	writeJSON(w, 200, s.view(r.Context(), u))
}

func (s *Server) handleUpstreamDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var wasPublic bool
	if old, err := s.st.GetUpstream(r.Context(), userFrom(r).ID, id); err == nil {
		wasPublic = old.IsPublic
	}
	if err := s.st.DeleteUpstream(r.Context(), userFrom(r).ID, id); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	s.up.Forget(id)
	s.upstreamsChanged(userFrom(r).ID, wasPublic)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleUpstreamTest(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUpstream(r.Context(), userFrom(r).ID, r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	st := s.up.Check(r.Context(), u)
	writeJSON(w, 200, st)
}

func (s *Server) handleUpstreamCatalog(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUpstream(r.Context(), userFrom(r).ID, r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	snap, err := s.up.Snapshot(r.Context(), u)
	if snap == nil {
		msg := "couldn't reach this MCP"
		if err != nil {
			msg = err.Error()
		}
		writeErr(w, 502, msg)
		return
	}
	type item struct {
		Name        string `json:"name"`
		GatewayName string `json:"gateway_name,omitempty"`
		Description string `json:"description"`
	}
	out := map[string][]item{"tools": {}, "prompts": {}, "resources": {}}
	for _, t := range snap.Tools {
		out["tools"] = append(out["tools"], item{t.Name, u.Slug + "__" + t.Name, t.Description})
	}
	for _, p := range snap.Prompts {
		out["prompts"] = append(out["prompts"], item{p.Name, u.Slug + "__" + p.Name, p.Description})
	}
	for _, rs := range snap.Resources {
		out["resources"] = append(out["resources"], item{rs.Name, "", rs.Description})
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleUpstreamOAuthStart(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUpstream(r.Context(), userFrom(r).ID, r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	if u.AuthType != "oauth" {
		writeErr(w, 400, "this MCP isn't set to use OAuth")
		return
	}
	var in struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if r.ContentLength > 0 && !readJSON(w, r, &in) {
		return
	}
	redirect := s.cfg.BaseURL + "/oauth/upstream/callback"
	authURL, err := s.up.StartOAuth(r.Context(), u, redirect, strings.TrimSpace(in.ClientID), strings.TrimSpace(in.ClientSecret))
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"url": authURL})
}

func (s *Server) handleUpstreamOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		http.Redirect(w, r, "/mcps?error="+url.QueryEscape("The MCP's login was cancelled: "+e), http.StatusFound)
		return
	}
	id, err := s.up.FinishOAuth(r.Context(), q.Get("state"), q.Get("code"), userFrom(r).ID)
	if err != nil {
		http.Redirect(w, r, "/mcps?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	s.upstreamsChanged(userFrom(r).ID, false)
	http.Redirect(w, r, "/mcps?connected="+url.QueryEscape(id), http.StatusFound)
}

func (s *Server) handleUpstreamOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUpstream(r.Context(), userFrom(r).ID, r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	_ = s.st.DeleteUpstreamOAuth(r.Context(), u.ID)
	s.up.Forget(u.ID)
	s.upstreamsChanged(u.UserID, u.IsPublic)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- bearer tokens ----

func (s *Server) handleTokenList(w http.ResponseWriter, r *http.Request) {
	ts, err := s.st.ListAPITokens(r.Context(), userFrom(r).ID)
	if err != nil {
		writeErr(w, 500, "couldn't load tokens")
		return
	}
	if ts == nil {
		ts = []*store.APIToken{}
	}
	writeJSON(w, 200, ts)
}

func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string    `json:"name"`
		UpstreamIDs   *[]string `json:"upstream_ids"`
		ExpiresInDays int       `json:"expires_in_days"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		writeErr(w, 400, "Give the token a name, like “Claude Desktop”.")
		return
	}
	u := userFrom(r)
	t := &store.APIToken{UserID: u.ID, Name: in.Name}
	if in.UpstreamIDs != nil {
		own, _ := s.st.ListUpstreams(r.Context(), u.ID)
		valid := map[string]bool{}
		for _, o := range own {
			valid[o.ID] = true
		}
		t.UpstreamIDs = []string{}
		for _, id := range *in.UpstreamIDs {
			if !valid[id] {
				writeErr(w, 400, "unknown MCP in token scope")
				return
			}
			t.UpstreamIDs = append(t.UpstreamIDs, id)
		}
	}
	if in.ExpiresInDays > 0 {
		e := time.Now().AddDate(0, 0, in.ExpiresInDays)
		t.ExpiresAt = &e
	}
	plain := "mcpt_" + crypto.RandToken(32)
	t.Prefix = plain[:10]
	if err := s.st.CreateAPIToken(r.Context(), t, plain); err != nil {
		writeErr(w, 500, "couldn't create the token")
		return
	}
	writeJSON(w, 201, map[string]any{"token": plain, "meta": t})
}

func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteAPIToken(r.Context(), userFrom(r).ID, r.PathValue("id")); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- OAuth grants (apps the user signed in to the gateway with) ----

func (s *Server) handleGrantList(w http.ResponseWriter, r *http.Request) {
	gs, err := s.st.ListGrants(r.Context(), userFrom(r).ID)
	if err != nil {
		writeErr(w, 500, "couldn't load apps")
		return
	}
	if gs == nil {
		gs = []*store.Grant{}
	}
	writeJSON(w, 200, gs)
}

func (s *Server) handleGrantRevoke(w http.ResponseWriter, r *http.Request) {
	_ = s.st.RevokeGrant(r.Context(), userFrom(r).ID, r.PathValue("client_id"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- admin ----

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.st.ListUsers(r.Context())
	if err != nil {
		writeErr(w, 500, "couldn't load users")
		return
	}
	sort.Slice(us, func(i, j int) bool { return us[i].CreatedAt.Before(us[j].CreatedAt) })
	writeJSON(w, 200, us)
}

func (s *Server) handleAdminUserCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email   string `json:"email"`
		Name    string `json:"name"`
		IsAdmin bool   `json:"is_admin"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !strings.Contains(in.Email, "@") || len(in.Email) > 254 {
		writeErr(w, 400, "Enter a valid email address.")
		return
	}
	u, err := s.st.CreateUser(r.Context(), in.Email, strings.TrimSpace(in.Name), in.IsAdmin)
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, 409, "That email already has an account.")
		return
	}
	if err != nil {
		writeErr(w, 500, "couldn't create the user")
		return
	}
	writeJSON(w, 201, u)
}

func (s *Server) handleAdminUserUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	me := userFrom(r)
	var in struct {
		Name     *string `json:"name"`
		IsAdmin  *bool   `json:"is_admin"`
		Disabled *bool   `json:"disabled"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	target, err := s.st.UserByID(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	if id == me.ID && ((in.IsAdmin != nil && !*in.IsAdmin) || (in.Disabled != nil && *in.Disabled)) {
		writeErr(w, 400, "You can't demote or disable yourself.")
		return
	}
	if target.IsAdmin && ((in.IsAdmin != nil && !*in.IsAdmin) || (in.Disabled != nil && *in.Disabled)) {
		if n, _ := s.st.AdminCount(r.Context()); n <= 1 {
			writeErr(w, 400, "There must be at least one active admin.")
			return
		}
	}
	if err := s.st.UpdateUser(r.Context(), id, in.Name, in.IsAdmin, in.Disabled); err != nil {
		writeErr(w, 500, "couldn't update")
		return
	}
	u, _ := s.st.UserByID(r.Context(), id)
	writeJSON(w, 200, u)
}

func (s *Server) handleAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == userFrom(r).ID {
		writeErr(w, 400, "You can't delete yourself.")
		return
	}
	target, err := s.st.UserByID(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	if target.IsAdmin && !target.Disabled {
		if n, _ := s.st.AdminCount(r.Context()); n <= 1 {
			writeErr(w, 400, "There must be at least one active admin.")
			return
		}
	}
	ups, _ := s.st.ListUpstreams(r.Context(), id)
	if err := s.st.DeleteUser(r.Context(), id); err != nil {
		writeErr(w, 500, "couldn't delete")
		return
	}
	for _, u := range ups {
		s.up.Forget(u.ID)
	}
	s.upstreamsChanged("", true)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{
		"open_signup":     s.st.GetBool(r.Context(), settingOpenSignup),
		"allow_anonymous": s.st.GetBool(r.Context(), settingAllowAnonymous),
	})
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OpenSignup     *bool `json:"open_signup"`
		AllowAnonymous *bool `json:"allow_anonymous"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	set := func(k string, v *bool) {
		if v != nil {
			val := "false"
			if *v {
				val = "true"
			}
			_ = s.st.SetSetting(r.Context(), k, val)
		}
	}
	set(settingOpenSignup, in.OpenSignup)
	set(settingAllowAnonymous, in.AllowAnonymous)
	s.handleSettingsGet(w, r)
}
