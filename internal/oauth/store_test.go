package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func persistedConfig(t *testing.T) Config {
	t.Helper()
	return Config{PublicURL: "https://tiimo.example", ClientID: "private-client", ClientSecret: strings.Repeat("s", 32), Password: strings.Repeat("p", 32), DBPath: filepath.Join(t.TempDir(), "auth", "auth.db")}
}
func TestSQLiteTokensSurviveRestart(t *testing.T) {
	ctx := context.Background()
	cfg := persistedConfig(t)
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	access, refresh, err := p.store.issue(ctx, cfg.PublicURL+"/mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	handler := p.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 204 {
		t.Fatalf("access lost after restart: %d", res.Code)
	}
	nextAccess, nextRefresh, err := p.store.issue(ctx, cfg.PublicURL+"/mcp", digest(refresh))
	if err != nil {
		t.Fatal("refresh lost after restart", err)
	}
	if _, _, err := p.store.issue(ctx, cfg.PublicURL+"/mcp", digest(refresh)); !errors.Is(err, errInvalidGrant) {
		t.Fatal("consumed refresh accepted", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	valid, err := p.store.valid(ctx, digest(nextAccess), cfg.PublicURL+"/mcp")
	if err != nil || !valid {
		t.Fatal("rotated access not persisted", err)
	}
	if _, _, err := p.store.issue(ctx, cfg.PublicURL+"/mcp", digest(refresh)); !errors.Is(err, errInvalidGrant) {
		t.Fatal("old refresh revived after restart")
	}
	if _, _, err := p.store.issue(ctx, cfg.PublicURL+"/mcp", digest(nextRefresh)); err != nil {
		t.Fatal("rotated refresh not persisted", err)
	}
}
func TestSQLiteStoresHashesAndPrivateFiles(t *testing.T) {
	cfg := persistedConfig(t)
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	a, f, err := p.store.issue(context.Background(), cfg.PublicURL+"/mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := p.store.db.Query(`SELECT hash FROM oauth_tokens`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatal(err)
		}
		if hash != digest(a) && hash != digest(f) {
			t.Fatal("unexpected token hash")
		}
		if hash == a || hash == f {
			t.Fatal("raw token persisted")
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatal("missing tokens")
	}
	if info, err := os.Stat(cfg.DBPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("database permissions", info, err)
	}
	if info, err := os.Stat(filepath.Dir(cfg.DBPath)); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("directory permissions", info, err)
	}
}
func TestSQLiteRejectsExpiredAndWrongResource(t *testing.T) {
	p, err := New(persistedConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()
	resource := p.cfg.PublicURL + "/mcp"
	a, f, err := p.store.issue(ctx, resource, "")
	if err != nil {
		t.Fatal(err)
	}
	if valid, err := p.store.valid(ctx, digest(a), "https://other.example/mcp"); err != nil || valid {
		t.Fatal("wrong audience accepted")
	}
	if _, _, err := p.store.issue(ctx, "https://other.example/mcp", digest(f)); !errors.Is(err, errInvalidGrant) {
		t.Fatal("wrong refresh audience accepted")
	}
	if _, err := p.store.db.Exec(`UPDATE oauth_tokens SET expires_at=?`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if valid, err := p.store.valid(ctx, digest(a), resource); err != nil || valid {
		t.Fatal("expired access accepted")
	}
	if _, _, err := p.store.issue(ctx, resource, digest(f)); !errors.Is(err, errInvalidGrant) {
		t.Fatal("expired refresh accepted")
	}
}
func TestSQLiteRefreshRollbackAndConcurrentReplay(t *testing.T) {
	p, err := New(persistedConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()
	resource := p.cfg.PublicURL + "/mcp"
	_, refresh, err := p.store.issue(ctx, resource, "")
	if err != nil {
		t.Fatal(err)
	}
	// Inject a write failure after the refresh DELETE. The transaction must restore it.
	if _, err := p.store.db.Exec(`CREATE TRIGGER fail_token_insert BEFORE INSERT ON oauth_tokens BEGIN SELECT RAISE(ABORT,'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.store.issue(ctx, resource, digest(refresh)); err == nil {
		t.Fatal("write failure ignored")
	}
	if _, err := p.store.db.Exec(`DROP TRIGGER fail_token_insert`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { ; _, _, err := p.store.issue(ctx, resource, digest(refresh)); results <- err })
	}
	wg.Wait()
	close(results)
	successes, replays := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, errInvalidGrant) {
			replays++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || replays != 1 {
		t.Fatalf("rotation not atomic: %d successes %d replays", successes, replays)
	}
}
func TestSQLiteConfigurationChangesRevokeTokens(t *testing.T) {
	cfg := persistedConfig(t)
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, f, err := p.store.issue(context.Background(), cfg.PublicURL+"/mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Close()
	cfg.ClientSecret = strings.Repeat("n", 32)
	p, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if valid, err := p.store.valid(context.Background(), digest(a), cfg.PublicURL+"/mcp"); err != nil || valid {
		t.Fatal("changed config kept old access")
	}
	if _, _, err := p.store.issue(context.Background(), cfg.PublicURL+"/mcp", digest(f)); !errors.Is(err, errInvalidGrant) {
		t.Fatal("changed config kept old refresh")
	}
}
func TestSQLiteInvalidStorageFailsClosed(t *testing.T) {
	cfg := persistedConfig(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(cfg.DBPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg); err == nil {
		t.Fatal("invalid storage silently accepted")
	}
}
