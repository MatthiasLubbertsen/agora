// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/matthiaslubbertsen/mcp-full-off-mcps/internal/crypto"
)

// Provider is one OIDC login provider for the website.
type Provider struct {
	ID           string // url-safe id, e.g. "hackclub"
	Label        string
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

type Config struct {
	BaseURL               string // public URL, no trailing slash
	Addr                  string
	DataDir               string
	SecretKey             string
	AllowPrivateUpstreams bool
	TrustProxy            bool
	Providers             []Provider
}

func (c *Config) Secure() bool { return strings.HasPrefix(c.BaseURL, "https://") }

func Load() (*Config, error) {
	c := &Config{
		BaseURL:               strings.TrimRight(os.Getenv("BASE_URL"), "/"),
		Addr:                  getenv("ADDR", ":8080"),
		DataDir:               getenv("DATA_DIR", "./data"),
		SecretKey:             os.Getenv("SECRET_KEY"),
		AllowPrivateUpstreams: os.Getenv("ALLOW_PRIVATE_UPSTREAMS") == "true",
		TrustProxy:            os.Getenv("TRUST_PROXY") == "true",
	}
	if c.BaseURL == "" {
		return nil, fmt.Errorf("BASE_URL is required (the public URL of this app, e.g. https://mcp.example.com)")
	}
	if !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
		return nil, fmt.Errorf("BASE_URL must start with http:// or https://")
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, err
	}
	if c.SecretKey == "" {
		// No key supplied: generate one once and keep it next to the database.
		path := filepath.Join(c.DataDir, "secret.key")
		if b, err := os.ReadFile(path); err == nil {
			c.SecretKey = strings.TrimSpace(string(b))
		} else {
			c.SecretKey = crypto.RandToken(32)
			if err := os.WriteFile(path, []byte(c.SecretKey), 0o600); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range strings.Split(os.Getenv("OIDC_PROVIDERS"), ",") {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		for _, r := range id {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return nil, fmt.Errorf("OIDC provider id %q: use only a-z, 0-9 and -", id)
			}
		}
		key := "OIDC_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_"
		p := Provider{
			ID:           id,
			Label:        getenv(key+"LABEL", id),
			Issuer:       strings.TrimRight(os.Getenv(key+"ISSUER"), "/"),
			ClientID:     os.Getenv(key + "CLIENT_ID"),
			ClientSecret: os.Getenv(key + "CLIENT_SECRET"),
			Scopes:       strings.Fields(getenv(key+"SCOPES", "openid email profile")),
		}
		if p.Issuer == "" || p.ClientID == "" {
			return nil, fmt.Errorf("provider %q needs %sISSUER and %sCLIENT_ID", id, key, key)
		}
		c.Providers = append(c.Providers, p)
	}
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
