package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/matthiaslubbertsen/mcp-full-of-mcps/internal/crypto"
)

// A database made before the label column existed must open and keep its rows.
func TestMigrateAddsLabelColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	old := `CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE, name TEXT NOT NULL DEFAULT '', is_admin INTEGER NOT NULL DEFAULT 0, disabled INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, last_login_at INTEGER);
CREATE TABLE upstreams (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, slug TEXT NOT NULL, name TEXT NOT NULL, url TEXT NOT NULL, transport TEXT NOT NULL DEFAULT 'http', auth_type TEXT NOT NULL DEFAULT 'none', header_name TEXT NOT NULL DEFAULT '', secret TEXT NOT NULL DEFAULT '', is_public INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, UNIQUE(user_id, slug));
INSERT INTO users(id,email,created_at) VALUES('u1','a@b.c',1);
INSERT INTO upstreams(id,user_id,slug,name,url,created_at) VALUES('x1','u1','exa','Exa','https://exa.example/mcp',1);`
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	db.Close()

	box, _ := crypto.NewBox("k")
	s, err := Open(dir, box)
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	defer s.Close()
	ups, err := s.ListUpstreams(context.Background(), "u1")
	if err != nil || len(ups) != 1 || ups[0].DisplayLabel() != "exa" {
		t.Fatalf("after migrate: %v %+v", err, ups)
	}
}
