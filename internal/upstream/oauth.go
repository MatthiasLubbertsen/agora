package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/matthiaslubbertsen/mcp-full-off-mcps/internal/crypto"
	"github.com/matthiaslubbertsen/mcp-full-off-mcps/internal/store"
)

type flow struct {
	upstreamID string
	userID     string
	verifier   string
	cfg        *oauth2.Config
	resource   string
	scopes     string
	expires    time.Time
}

type flows struct {
	mu sync.Mutex
	m  map[string]*flow
}

var pending = &flows{m: map[string]*flow{}}

func (f *flows) put(state string, fl *flow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range f.m {
		if time.Now().After(v.expires) {
			delete(f.m, k)
		}
	}
	f.m[state] = fl
}

func (f *flows) take(state string) *flow {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl := f.m[state]
	delete(f.m, state)
	if fl == nil || time.Now().After(fl.expires) {
		return nil
	}
	return fl
}

func getJSON(ctx context.Context, c *http.Client, u string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return 200, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// discoverResourceMetadataURL finds the RFC 9728 metadata URL for an MCP server,
// first via its 401 challenge and then via well-known locations.
func (m *Manager) discoverResource(ctx context.Context, mcpURL string) (*oauthex.ProtectedResourceMetadata, error) {
	u, err := url.Parse(mcpURL)
	if err != nil {
		return nil, err
	}
	origin := u.Scheme + "://" + u.Host
	var candidates []string

	probe, _ := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL,
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcp-gateway","version":"1"}}}`)))
	probe.Header.Set("Content-Type", "application/json")
	probe.Header.Set("Accept", "application/json, text/event-stream")
	if resp, err := m.httpc.Do(probe); err == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if chs, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate")); err == nil {
			for _, c := range chs {
				if v := c.Params["resource_metadata"]; v != "" {
					candidates = append(candidates, v)
				}
			}
		}
	}
	if p := strings.TrimRight(u.Path, "/"); p != "" {
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource"+p)
	}
	candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")

	for _, c := range candidates {
		var prm oauthex.ProtectedResourceMetadata
		if _, err := getJSON(ctx, m.httpc, c, &prm); err == nil && len(prm.AuthorizationServers) > 0 {
			return &prm, nil
		}
	}
	// No resource metadata: assume the MCP server is its own authorization server.
	return &oauthex.ProtectedResourceMetadata{Resource: mcpURL, AuthorizationServers: []string{origin}}, nil
}

func (m *Manager) discoverAS(ctx context.Context, issuer string) (*oauthex.AuthServerMeta, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, err
	}
	origin := u.Scheme + "://" + u.Host
	p := strings.TrimRight(u.Path, "/")
	var urls []string
	if p == "" {
		urls = []string{origin + "/.well-known/oauth-authorization-server", origin + "/.well-known/openid-configuration"}
	} else {
		urls = []string{origin + "/.well-known/oauth-authorization-server" + p, origin + "/.well-known/openid-configuration" + p,
			strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"}
	}
	var lastErr error
	for _, mu := range urls {
		meta, err := oauthex.GetAuthServerMeta(ctx, mu, issuer, m.httpc)
		if err != nil {
			lastErr = err
			continue
		}
		if meta != nil {
			return meta, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no authorization server metadata found")
}

// StartOAuth discovers the upstream's authorization server, registers this
// gateway as a client if needed, and returns the URL the user must visit.
func (m *Manager) StartOAuth(ctx context.Context, up *store.Upstream, redirectURI, presetID, presetSecret string) (string, error) {
	prm, err := m.discoverResource(ctx, up.URL)
	if err != nil {
		return "", err
	}
	issuer := prm.AuthorizationServers[0]
	as, err := m.discoverAS(ctx, issuer)
	if err != nil {
		return "", fmt.Errorf("could not find the login server for this MCP: %w", err)
	}

	clientID, clientSecret := presetID, presetSecret
	if clientID == "" {
		// Reuse an earlier registration if it was for the same server and redirect URI.
		if old, err := m.store.GetUpstreamOAuth(ctx, up.ID); err == nil && old.AuthURL == as.AuthorizationEndpoint && old.ClientID != "" {
			clientID, clientSecret = old.ClientID, old.ClientSecret
		}
	}
	if clientID == "" {
		if as.RegistrationEndpoint == "" {
			return "", errors.New("this MCP's login server doesn't allow automatic setup; create an OAuth app there and enter its client ID and secret")
		}
		reg, err := oauthex.RegisterClient(ctx, as.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
			RedirectURIs:            []string{redirectURI},
			TokenEndpointAuthMethod: "none",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			ClientName:              "MCP Gateway",
			ApplicationType:         "web",
		}, m.httpc)
		if err != nil {
			return "", fmt.Errorf("registering with the login server failed: %w", err)
		}
		clientID, clientSecret = reg.ClientID, reg.ClientSecret
	}

	scopes := strings.Join(prm.ScopesSupported, " ")
	cfg := &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURI, Scopes: prm.ScopesSupported,
		Endpoint: oauth2.Endpoint{AuthURL: as.AuthorizationEndpoint, TokenURL: as.TokenEndpoint, AuthStyle: oauth2.AuthStyleAutoDetect}}
	state := crypto.RandToken(24)
	verifier := oauth2.GenerateVerifier()
	resource := prm.Resource
	if resource == "" {
		resource = up.URL
	}
	pending.put(state, &flow{upstreamID: up.ID, userID: up.UserID, verifier: verifier, cfg: cfg, resource: resource, scopes: scopes,
		expires: time.Now().Add(10 * time.Minute)})
	return cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", resource)), nil
}

// FinishOAuth completes the flow from the redirect and stores the tokens.
// userID must be the logged-in user who started it.
func (m *Manager) FinishOAuth(ctx context.Context, state, code, userID string) (string, error) {
	fl := pending.take(state)
	if fl == nil {
		return "", errors.New("this login link expired, start again")
	}
	if fl.userID != userID {
		return "", errors.New("this login was started by a different user")
	}
	hctx := context.WithValue(ctx, oauth2.HTTPClient, m.httpc)
	tok, err := fl.cfg.Exchange(hctx, code, oauth2.VerifierOption(fl.verifier), oauth2.SetAuthURLParam("resource", fl.resource))
	if err != nil {
		return "", fmt.Errorf("token exchange failed: %w", err)
	}
	b, _ := json.Marshal(tok)
	if err := m.store.PutUpstreamOAuth(ctx, &store.UpstreamOAuth{UpstreamID: fl.upstreamID, ClientID: fl.cfg.ClientID,
		ClientSecret: fl.cfg.ClientSecret, AuthURL: fl.cfg.Endpoint.AuthURL, TokenURL: fl.cfg.Endpoint.TokenURL,
		Resource: fl.resource, Scopes: fl.scopes, Token: string(b)}); err != nil {
		return "", err
	}
	m.Forget(fl.upstreamID) // reconnect with the new credentials
	return fl.upstreamID, nil
}

// Connected reports whether an OAuth upstream holds a token.
func (m *Manager) Connected(ctx context.Context, upstreamID string) bool {
	o, err := m.store.GetUpstreamOAuth(ctx, upstreamID)
	return err == nil && o.Token != ""
}
