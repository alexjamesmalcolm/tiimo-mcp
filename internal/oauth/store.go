package oauth

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var errInvalidGrant = errors.New("invalid grant")
var errCapacity = errors.New("token capacity reached")

type tokenStore struct {
	db      *sql.DB
	binding string
}

// Only SHA-256 hashes of randomly generated bearer tokens are persisted.
// No Tiimo token, login password, client secret, or raw bearer token is stored.
func openStore(path, binding string) (*tokenStore, error) {
	dsn := ":memory:?_busy_timeout=5000&_foreign_keys=on&_synchronous=FULL&_txlock=immediate"
	if path != "" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		dir := filepath.Dir(absolute)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("auth database directory must be a real private directory")
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, err
		}
		if info, err := os.Lstat(absolute); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return nil, errors.New("auth database must be a regular file")
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		f, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err := f.Chmod(0600); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		u := url.URL{Scheme: "file", Path: absolute}
		q := url.Values{"_busy_timeout": {"5000"}, "_foreign_keys": {"on"}, "_journal_mode": {"WAL"}, "_synchronous": {"FULL"}, "_txlock": {"immediate"}}
		u.RawQuery = q.Encode()
		dsn = u.String()
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &tokenStore{db: db, binding: binding}
	if err := store.initialize(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}
func (s *tokenStore) initialize() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return errors.New("auth database schema is newer than this server")
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS oauth_meta (id INTEGER PRIMARY KEY CHECK(id=1), binding TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS oauth_tokens (kind TEXT NOT NULL CHECK(kind IN ('access','refresh')), hash TEXT NOT NULL, resource TEXT NOT NULL, expires_at INTEGER NOT NULL, binding TEXT NOT NULL, PRIMARY KEY(kind,hash))`,
		`CREATE INDEX IF NOT EXISTS oauth_tokens_expiry ON oauth_tokens(expires_at)`,
		`PRAGMA user_version=1`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	var existing string
	err = tx.QueryRow(`SELECT binding FROM oauth_meta WHERE id=1`).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existing != s.binding {
		if _, err := tx.Exec(`DELETE FROM oauth_tokens`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO oauth_meta(id,binding) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET binding=excluded.binding`, s.binding); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM oauth_tokens WHERE expires_at<=?`, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *tokenStore) close() error { return s.db.Close() }
func (s *tokenStore) valid(ctx context.Context, hash, resource string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM oauth_tokens WHERE kind='access' AND hash=? AND resource=? AND binding=? AND expires_at>?`, hash, resource, s.binding, time.Now().Unix()).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// issue commits the replacement pair and consumption of a refresh token in one
// transaction. Failed writes leave the old refresh token usable; replay is rejected.
func (s *tokenStore) issue(ctx context.Context, resource, refreshHash string) (string, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE expires_at<=?`, now.Unix()); err != nil {
		return "", "", err
	}
	if refreshHash != "" {
		result, err := tx.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE kind='refresh' AND hash=? AND resource=? AND binding=? AND expires_at>?`, refreshHash, resource, s.binding, now.Unix())
		if err != nil {
			return "", "", err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return "", "", err
		}
		if n != 1 {
			return "", "", errInvalidGrant
		}
	}
	for _, kind := range []string{"access", "refresh"} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_tokens WHERE kind=?`, kind).Scan(&count); err != nil {
			return "", "", err
		}
		if count >= 512 {
			return "", "", errCapacity
		}
	}
	access, refresh := random(), random()
	for _, v := range []struct {
		kind, raw string
		expiry    time.Time
	}{{"access", access, now.Add(time.Hour)}, {"refresh", refresh, now.Add(7 * 24 * time.Hour)}} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_tokens(kind,hash,resource,expires_at,binding) VALUES(?,?,?,?,?)`, v.kind, digest(v.raw), resource, v.expiry.Unix(), s.binding); err != nil {
			return "", "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return access, refresh, nil
}
