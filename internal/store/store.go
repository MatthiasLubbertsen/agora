// Package store is the SQLite persistence layer.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/crypto"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("already exists")

type Store struct {
	db  *sql.DB
	box *crypto.Box
}

func Open(dataDir string, box *crypto.Box) (*Store, error) {
	dsn := "file:" + filepath.Join(dataDir, "gateway.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	s := &Store{db: db, box: box}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL DEFAULT '',
  is_admin INTEGER NOT NULL DEFAULT 0,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  last_login_at INTEGER
);
CREATE TABLE IF NOT EXISTS sessions (
  id_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS upstreams (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL,
  transport TEXT NOT NULL DEFAULT 'http',
  auth_type TEXT NOT NULL DEFAULT 'none',
  header_name TEXT NOT NULL DEFAULT '',
  secret TEXT NOT NULL DEFAULT '',
  is_public INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  UNIQUE(user_id, slug)
);
CREATE TABLE IF NOT EXISTS upstream_oauth (
  upstream_id TEXT PRIMARY KEY REFERENCES upstreams(id) ON DELETE CASCADE,
  client_id TEXT NOT NULL,
  client_secret TEXT NOT NULL DEFAULT '',
  auth_url TEXT NOT NULL,
  token_url TEXT NOT NULL,
  resource TEXT NOT NULL DEFAULT '',
  scopes TEXT NOT NULL DEFAULT '',
  token TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS api_tokens (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  hash TEXT NOT NULL UNIQUE,
  upstream_ids TEXT,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER,
  expires_at INTEGER
);
CREATE TABLE IF NOT EXISTS oauth_clients (
  client_id TEXT PRIMARY KEY,
  secret_hash TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  redirect_uris TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_codes (
  code_hash TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  redirect_uri TEXT NOT NULL,
  code_challenge TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_tokens (
  id TEXT PRIMARY KEY,
  access_hash TEXT NOT NULL UNIQUE,
  refresh_hash TEXT NOT NULL UNIQUE,
  client_id TEXT NOT NULL,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  access_expires_at INTEGER NOT NULL,
  refresh_expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_upstreams_user ON upstreams(user_id);
CREATE INDEX IF NOT EXISTS idx_oauth_tokens_user ON oauth_tokens(user_id);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Databases created before the label column existed.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('upstreams') WHERE name='label'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.db.Exec(`ALTER TABLE upstreams ADD COLUMN label TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

func ts(t time.Time) int64 { return t.Unix() }
func fromTS(n int64) time.Time {
	return time.Unix(n, 0)
}
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ---- settings ----

func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) GetBool(ctx context.Context, key string) bool {
	v, ok, _ := s.GetSetting(ctx, key)
	return ok && v == "true"
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---- users ----

type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	IsAdmin     bool      `json:"is_admin"`
	Disabled    bool      `json:"disabled"`
	CreatedAt   time.Time `json:"created_at"`
	LastLoginAt time.Time `json:"last_login_at"`
}

const userCols = `id,email,name,is_admin,disabled,created_at,COALESCE(last_login_at,0)`

func scanUser(r interface{ Scan(...any) error }) (*User, error) {
	var u User
	var admin, dis int
	var c, l int64
	if err := r.Scan(&u.ID, &u.Email, &u.Name, &admin, &dis, &c, &l); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.IsAdmin, u.Disabled, u.CreatedAt = admin == 1, dis == 1, fromTS(c)
	if l > 0 {
		u.LastLoginAt = fromTS(l)
	}
	return &u, nil
}

func (s *Store) CreateUser(ctx context.Context, email, name string, admin bool) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	u := &User{ID: crypto.RandHex(12), Email: email, Name: name, IsAdmin: admin, CreatedAt: time.Now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(id,email,name,is_admin,created_at) VALUES(?,?,?,?,?)`,
		u.ID, u.Email, u.Name, b2i(admin), ts(u.CreatedAt))
	if isUnique(err) {
		return nil, ErrConflict
	}
	return u, err
}

func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email=?`, strings.ToLower(strings.TrimSpace(email))))
}

func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin=1 AND disabled=0`).Scan(&n)
	return n, err
}

func (s *Store) UpdateUser(ctx context.Context, id string, name *string, admin, disabled *bool) error {
	if name != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE users SET name=? WHERE id=?`, *name, id); err != nil {
			return err
		}
	}
	if admin != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE users SET is_admin=? WHERE id=?`, b2i(*admin), id); err != nil {
			return err
		}
	}
	if disabled != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE users SET disabled=? WHERE id=?`, b2i(*disabled), id); err != nil {
			return err
		}
		if *disabled {
			// Cut off every credential the user holds.
			s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id)
			s.db.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE user_id=?`, id)
		}
	}
	return nil
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

func (s *Store) TouchLogin(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at=?, name=CASE WHEN name='' THEN ? ELSE name END WHERE id=?`,
		time.Now().Unix(), name, id)
	return err
}

// ---- web sessions ----

func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	tok := crypto.RandToken(32)
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(id_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`,
		crypto.Hash(tok), userID, ts(now.Add(ttl)), ts(now))
	return tok, err
}

// SessionUser returns the user for a session token, or ErrNotFound.
func (s *Store) SessionUser(ctx context.Context, tok string) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT u.id,u.email,u.name,u.is_admin,u.disabled,u.created_at,COALESCE(u.last_login_at,0) FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.id_hash=? AND s.expires_at>?`, crypto.Hash(tok), time.Now().Unix()))
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, ErrNotFound
	}
	return u, nil
}

func (s *Store) DeleteSession(ctx context.Context, tok string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash=?`, crypto.Hash(tok))
	return err
}

// ---- upstreams ----

type Upstream struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Slug       string    `json:"slug"`
	Name       string    `json:"name"`
	Label      string    `json:"label"` // short tag shown next to each tool; empty means use Slug
	URL        string    `json:"url"`
	Transport  string    `json:"transport"` // http | sse
	AuthType   string    `json:"auth_type"` // none | header | oauth
	HeaderName string    `json:"header_name"`
	Secret     string    `json:"-"` // plaintext in memory only
	IsPublic   bool      `json:"is_public"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
}

// DisplayLabel is the short tag shown next to this upstream's tools.
func (u *Upstream) DisplayLabel() string {
	if u.Label != "" {
		return u.Label
	}
	return u.Slug
}

type UpstreamOAuth struct {
	UpstreamID   string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Resource     string
	Scopes       string
	Token        string // JSON-encoded oauth2.Token, empty until connected
}

const upCols = `id,user_id,slug,name,label,url,transport,auth_type,header_name,secret,is_public,enabled,created_at`

func (s *Store) scanUpstream(r interface{ Scan(...any) error }) (*Upstream, error) {
	var u Upstream
	var pub, en int
	var c int64
	var sec string
	if err := r.Scan(&u.ID, &u.UserID, &u.Slug, &u.Name, &u.Label, &u.URL, &u.Transport, &u.AuthType, &u.HeaderName, &sec, &pub, &en, &c); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var err error
	if u.Secret, err = s.box.Open(sec); err != nil {
		return nil, fmt.Errorf("decrypt upstream secret: %w", err)
	}
	u.IsPublic, u.Enabled, u.CreatedAt = pub == 1, en == 1, fromTS(c)
	return &u, nil
}

func (s *Store) CreateUpstream(ctx context.Context, u *Upstream) error {
	u.ID = crypto.RandHex(12)
	u.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO upstreams(`+upCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.UserID, u.Slug, u.Name, u.Label, u.URL, u.Transport, u.AuthType, u.HeaderName, s.box.Seal(u.Secret),
		b2i(u.IsPublic), b2i(u.Enabled), ts(u.CreatedAt))
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UpdateUpstream(ctx context.Context, u *Upstream) error {
	res, err := s.db.ExecContext(ctx, `UPDATE upstreams SET slug=?,name=?,label=?,url=?,transport=?,auth_type=?,header_name=?,secret=?,is_public=?,enabled=?
		WHERE id=? AND user_id=?`,
		u.Slug, u.Name, u.Label, u.URL, u.Transport, u.AuthType, u.HeaderName, s.box.Seal(u.Secret), b2i(u.IsPublic), b2i(u.Enabled), u.ID, u.UserID)
	if isUnique(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetUpstream(ctx context.Context, userID, id string) (*Upstream, error) {
	return s.scanUpstream(s.db.QueryRowContext(ctx, `SELECT `+upCols+` FROM upstreams WHERE id=? AND user_id=?`, id, userID))
}

// GetUpstreamAny is for internal callers that already authorised the request.
func (s *Store) GetUpstreamAny(ctx context.Context, id string) (*Upstream, error) {
	return s.scanUpstream(s.db.QueryRowContext(ctx, `SELECT `+upCols+` FROM upstreams WHERE id=?`, id))
}

func (s *Store) ListUpstreams(ctx context.Context, userID string) ([]*Upstream, error) {
	return s.listUpstreams(ctx, `SELECT `+upCols+` FROM upstreams WHERE user_id=? ORDER BY slug`, userID)
}

// ListPublicUpstreams returns the enabled upstreams anyone may use without logging in.
func (s *Store) ListPublicUpstreams(ctx context.Context) ([]*Upstream, error) {
	return s.listUpstreams(ctx, `SELECT `+upCols+` FROM upstreams WHERE is_public=1 AND enabled=1 AND user_id IN (SELECT id FROM users WHERE disabled=0) ORDER BY user_id, slug`)
}

func (s *Store) listUpstreams(ctx context.Context, q string, args ...any) ([]*Upstream, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Upstream
	for rows.Next() {
		u, err := s.scanUpstream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUpstream(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM upstreams WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetUpstreamOAuth(ctx context.Context, upstreamID string) (*UpstreamOAuth, error) {
	var o UpstreamOAuth
	var sec, tok string
	err := s.db.QueryRowContext(ctx, `SELECT upstream_id,client_id,client_secret,auth_url,token_url,resource,scopes,token FROM upstream_oauth WHERE upstream_id=?`, upstreamID).
		Scan(&o.UpstreamID, &o.ClientID, &sec, &o.AuthURL, &o.TokenURL, &o.Resource, &o.Scopes, &tok)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if o.ClientSecret, err = s.box.Open(sec); err != nil {
		return nil, err
	}
	if o.Token, err = s.box.Open(tok); err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) PutUpstreamOAuth(ctx context.Context, o *UpstreamOAuth) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO upstream_oauth(upstream_id,client_id,client_secret,auth_url,token_url,resource,scopes,token)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(upstream_id) DO UPDATE SET client_id=excluded.client_id,client_secret=excluded.client_secret,
		auth_url=excluded.auth_url,token_url=excluded.token_url,resource=excluded.resource,scopes=excluded.scopes,token=excluded.token`,
		o.UpstreamID, o.ClientID, s.box.Seal(o.ClientSecret), o.AuthURL, o.TokenURL, o.Resource, o.Scopes, s.box.Seal(o.Token))
	return err
}

func (s *Store) SaveUpstreamToken(ctx context.Context, upstreamID, tokenJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE upstream_oauth SET token=? WHERE upstream_id=?`, s.box.Seal(tokenJSON), upstreamID)
	return err
}

func (s *Store) DeleteUpstreamOAuth(ctx context.Context, upstreamID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM upstream_oauth WHERE upstream_id=?`, upstreamID)
	return err
}

// ---- API (bearer) tokens ----

type APIToken struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	Prefix      string     `json:"prefix"`
	UpstreamIDs []string   `json:"upstream_ids"` // nil = all of the user's MCPs
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

func (s *Store) CreateAPIToken(ctx context.Context, t *APIToken, plaintext string) error {
	t.ID = crypto.RandHex(12)
	t.CreatedAt = time.Now()
	var ids any
	if t.UpstreamIDs != nil {
		b, _ := json.Marshal(t.UpstreamIDs)
		ids = string(b)
	}
	var exp any
	if t.ExpiresAt != nil {
		exp = ts(*t.ExpiresAt)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens(id,user_id,name,prefix,hash,upstream_ids,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`,
		t.ID, t.UserID, t.Name, t.Prefix, crypto.Hash(plaintext), ids, ts(t.CreatedAt), exp)
	return err
}

func scanToken(r interface{ Scan(...any) error }) (*APIToken, error) {
	var t APIToken
	var ids sql.NullString
	var c int64
	var last, exp sql.NullInt64
	if err := r.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &ids, &c, &last, &exp); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if ids.Valid {
		t.UpstreamIDs = []string{}
		_ = json.Unmarshal([]byte(ids.String), &t.UpstreamIDs)
	}
	t.CreatedAt = fromTS(c)
	if last.Valid {
		v := fromTS(last.Int64)
		t.LastUsedAt = &v
	}
	if exp.Valid {
		v := fromTS(exp.Int64)
		t.ExpiresAt = &v
	}
	return &t, nil
}

const tokCols = `id,user_id,name,prefix,upstream_ids,created_at,last_used_at,expires_at`

func (s *Store) ListAPITokens(ctx context.Context, userID string) ([]*APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokCols+` FROM api_tokens WHERE user_id=? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LookupAPIToken resolves a plaintext token to its record and owner.
func (s *Store) LookupAPIToken(ctx context.Context, plaintext string) (*APIToken, *User, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokCols+` FROM api_tokens WHERE hash=?`, crypto.Hash(plaintext)))
	if err != nil {
		return nil, nil, err
	}
	if t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt) {
		return nil, nil, ErrNotFound
	}
	u, err := s.UserByID(ctx, t.UserID)
	if err != nil {
		return nil, nil, err
	}
	if u.Disabled {
		return nil, nil, ErrNotFound
	}
	s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, time.Now().Unix(), t.ID)
	return t, u, nil
}

func (s *Store) DeleteAPIToken(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- OAuth server (the gateway acting as authorization server) ----

type OAuthClient struct {
	ClientID     string
	SecretHash   string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

func (s *Store) CreateOAuthClient(ctx context.Context, c *OAuthClient) error {
	c.CreatedAt = time.Now()
	uris, _ := json.Marshal(c.RedirectURIs)
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_clients(client_id,secret_hash,name,redirect_uris,created_at) VALUES(?,?,?,?,?)`,
		c.ClientID, c.SecretHash, c.Name, string(uris), ts(c.CreatedAt))
	return err
}

func (s *Store) GetOAuthClient(ctx context.Context, id string) (*OAuthClient, error) {
	var c OAuthClient
	var uris string
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT client_id,secret_hash,name,redirect_uris,created_at FROM oauth_clients WHERE client_id=?`, id).
		Scan(&c.ClientID, &c.SecretHash, &c.Name, &uris, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(uris), &c.RedirectURIs)
	c.CreatedAt = fromTS(created)
	return &c, nil
}

type OAuthCode struct {
	ClientID, UserID, RedirectURI, CodeChallenge string
}

func (s *Store) CreateOAuthCode(ctx context.Context, code string, c *OAuthCode, ttl time.Duration) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_codes(code_hash,client_id,user_id,redirect_uri,code_challenge,expires_at) VALUES(?,?,?,?,?,?)`,
		crypto.Hash(code), c.ClientID, c.UserID, c.RedirectURI, c.CodeChallenge, time.Now().Add(ttl).Unix())
	return err
}

// ConsumeOAuthCode atomically fetches and deletes a code so it is single use.
func (s *Store) ConsumeOAuthCode(ctx context.Context, code string) (*OAuthCode, error) {
	var c OAuthCode
	var exp int64
	err := s.db.QueryRowContext(ctx, `DELETE FROM oauth_codes WHERE code_hash=? RETURNING client_id,user_id,redirect_uri,code_challenge,expires_at`,
		crypto.Hash(code)).Scan(&c.ClientID, &c.UserID, &c.RedirectURI, &c.CodeChallenge, &exp)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && time.Now().Unix() > exp) {
		return nil, ErrNotFound
	}
	return &c, err
}

type OAuthToken struct {
	ID, ClientID, UserID string
	AccessExpiresAt      time.Time
	RefreshExpiresAt     time.Time
	CreatedAt            time.Time
}

func (s *Store) CreateOAuthToken(ctx context.Context, access, refresh, clientID, userID string, accessTTL, refreshTTL time.Duration) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_tokens(id,access_hash,refresh_hash,client_id,user_id,access_expires_at,refresh_expires_at,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		crypto.RandHex(12), crypto.Hash(access), crypto.Hash(refresh), clientID, userID, now.Add(accessTTL).Unix(), now.Add(refreshTTL).Unix(), now.Unix())
	return err
}

// LookupOAuthAccess resolves an access token to its user.
func (s *Store) LookupOAuthAccess(ctx context.Context, access string) (*User, error) {
	var uid string
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id,access_expires_at FROM oauth_tokens WHERE access_hash=?`, crypto.Hash(access)).Scan(&uid, &exp)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && time.Now().Unix() > exp) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u, err := s.UserByID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, ErrNotFound
	}
	return u, nil
}

// RotateRefresh consumes a refresh token (single use) and returns what it was bound to.
func (s *Store) RotateRefresh(ctx context.Context, refresh string) (clientID, userID string, err error) {
	var exp int64
	err = s.db.QueryRowContext(ctx, `DELETE FROM oauth_tokens WHERE refresh_hash=? RETURNING client_id,user_id,refresh_expires_at`,
		crypto.Hash(refresh)).Scan(&clientID, &userID, &exp)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && time.Now().Unix() > exp) {
		return "", "", ErrNotFound
	}
	return
}

type Grant struct {
	ClientID   string    `json:"client_id"`
	ClientName string    `json:"client_name"`
	CreatedAt  time.Time `json:"created_at"`
}

// ListGrants lists the apps a user has authorised via OAuth.
func (s *Store) ListGrants(ctx context.Context, userID string) ([]*Grant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.client_id, COALESCE(c.name,t.client_id), MIN(t.created_at) FROM oauth_tokens t
		LEFT JOIN oauth_clients c ON c.client_id=t.client_id WHERE t.user_id=? GROUP BY t.client_id ORDER BY 3 DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Grant
	for rows.Next() {
		var g Grant
		var c int64
		if err := rows.Scan(&g.ClientID, &g.ClientName, &c); err != nil {
			return nil, err
		}
		g.CreatedAt = fromTS(c)
		out = append(out, &g)
	}
	return out, rows.Err()
}

func (s *Store) RevokeGrant(ctx context.Context, userID, clientID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE user_id=? AND client_id=?`, userID, clientID)
	return err
}

// Prune removes expired rows. Safe to call periodically.
func (s *Store) Prune(ctx context.Context) {
	now := time.Now().Unix()
	s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<?`, now)
	s.db.ExecContext(ctx, `DELETE FROM oauth_codes WHERE expires_at<?`, now)
	s.db.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE refresh_expires_at<?`, now)
	// Dynamically registered clients that never completed a login are junk.
	s.db.ExecContext(ctx, `DELETE FROM oauth_clients WHERE created_at<? AND client_id NOT IN (SELECT client_id FROM oauth_tokens)`,
		time.Now().Add(-24*time.Hour).Unix())
}
