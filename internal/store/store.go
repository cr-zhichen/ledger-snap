// Package store keeps users, sessions, catalogs, recognition records and
// learned rules in one SQLite database.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("记录不存在")

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS users (
	uid          TEXT PRIMARY KEY,
	account      TEXT NOT NULL UNIQUE,
	password_md5 TEXT NOT NULL,
	qj_token     TEXT NOT NULL,
	device       TEXT NOT NULL,
	invite       TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	catalog      TEXT NOT NULL DEFAULT '',
	catalog_at   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS tokens (
	hash       TEXT PRIMARY KEY,
	uid        TEXT NOT NULL REFERENCES users(uid),
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS records (
	id         TEXT PRIMARY KEY,
	uid        TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	status     TEXT NOT NULL,
	image_path TEXT NOT NULL DEFAULT '',
	draft      TEXT NOT NULL DEFAULT '',
	trace      TEXT NOT NULL DEFAULT '',
	bill_id    TEXT NOT NULL DEFAULT '',
	bill       TEXT NOT NULL DEFAULT '',
	summary    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS records_uid_time ON records(uid, created_at);
CREATE TABLE IF NOT EXISTS asset_aliases (
	uid        TEXT NOT NULL,
	pattern    TEXT NOT NULL,
	asset_id   TEXT NOT NULL,
	hits       INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (uid, pattern)
);
CREATE TABLE IF NOT EXISTS merchant_rules (
	uid         TEXT NOT NULL,
	merchant    TEXT NOT NULL,
	category_id TEXT NOT NULL,
	hits        INTEGER NOT NULL DEFAULT 0,
	updated_at  INTEGER NOT NULL,
	PRIMARY KEY (uid, merchant)
);`

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化数据库 %s（检查数据目录能否写入）: %w", path, err)
	}
	// Credentials live here, so keep the file private.
	_ = os.Chmod(path, 0o600)
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type User struct {
	UID         string
	Account     string
	PasswordMD5 string
	QJToken     string
	Device      string
	Invite      string
	CreatedAt   int64
	Catalog     string
	CatalogAt   int64
}

const userColumns = `uid, account, password_md5, qj_token, device, invite, created_at, catalog, catalog_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(&u.UID, &u.Account, &u.PasswordMD5, &u.QJToken, &u.Device, &u.Invite, &u.CreatedAt, &u.Catalog, &u.CatalogAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

func (s *Store) UserByAccount(ctx context.Context, account string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE account = ?`, account))
}

func (s *Store) UserByToken(ctx context.Context, hash string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE uid = (SELECT uid FROM tokens WHERE hash = ?)`, hash))
}

// SaveUser inserts or updates a user's login state.
func (s *Store) SaveUser(ctx context.Context, u *User) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (uid, account, password_md5, qj_token, device, invite, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(uid) DO UPDATE SET account = excluded.account, password_md5 = excluded.password_md5,
			qj_token = excluded.qj_token, device = excluded.device`,
		u.UID, u.Account, u.PasswordMD5, u.QJToken, u.Device, u.Invite, u.CreatedAt)
	return err
}

func (s *Store) SaveQianjiToken(ctx context.Context, uid, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET qj_token = ? WHERE uid = ?`, token, uid)
	return err
}

func (s *Store) SaveCatalog(ctx context.Context, uid, catalog string, at int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET catalog = ?, catalog_at = ? WHERE uid = ?`, catalog, at, uid)
	return err
}

func (s *Store) AddToken(ctx context.Context, hash, uid string, at int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens (hash, uid, created_at) VALUES (?, ?, ?)`, hash, uid, at)
	return err
}

type Record struct {
	ID        string
	UID       string
	CreatedAt int64
	Status    string // written | needs_confirm | rejected | deleted | discarded
	ImagePath string
	Draft     string
	Trace     string
	BillID    string
	Bill      string
	Summary   string
}

const recordColumns = `id, uid, created_at, status, image_path, draft, trace, bill_id, bill, summary`

func scanRecord(row interface{ Scan(...any) error }) (*Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.UID, &r.CreatedAt, &r.Status, &r.ImagePath, &r.Draft, &r.Trace, &r.BillID, &r.Bill, &r.Summary)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

func (s *Store) SaveRecord(ctx context.Context, r *Record) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO records (`+recordColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, image_path = excluded.image_path, draft = excluded.draft,
			trace = excluded.trace, bill_id = excluded.bill_id, bill = excluded.bill, summary = excluded.summary`,
		r.ID, r.UID, r.CreatedAt, r.Status, r.ImagePath, r.Draft, r.Trace, r.BillID, r.Bill, r.Summary)
	return err
}

func (s *Store) Record(ctx context.Context, uid, id string) (*Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, `SELECT `+recordColumns+` FROM records WHERE uid = ? AND id = ?`, uid, id))
}

// WrittenSince lists the user's written records created after a time.
func (s *Store) WrittenSince(ctx context.Context, uid string, since int64) ([]*Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM records WHERE uid = ? AND status = 'written' AND created_at >= ?`, uid, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClearImagesBefore detaches and returns image paths of records created before a time.
func (s *Store) ClearImagesBefore(ctx context.Context, before int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT image_path FROM records WHERE created_at < ? AND image_path != ''`, before)
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		paths = append(paths, p)
	}
	rows.Close()
	_, err = s.db.ExecContext(ctx, `UPDATE records SET image_path = '' WHERE created_at < ?`, before)
	return paths, err
}

type Memory struct {
	AssetAliases  map[string]string
	MerchantRules map[string]string
}

func (s *Store) Memory(ctx context.Context, uid string) (Memory, error) {
	m := Memory{AssetAliases: map[string]string{}, MerchantRules: map[string]string{}}
	for _, q := range []struct {
		sql string
		dst map[string]string
	}{
		{`SELECT pattern, asset_id FROM asset_aliases WHERE uid = ?`, m.AssetAliases},
		{`SELECT merchant, category_id FROM merchant_rules WHERE uid = ?`, m.MerchantRules},
	} {
		rows, err := s.db.QueryContext(ctx, q.sql, uid)
		if err != nil {
			return m, err
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				rows.Close()
				return m, err
			}
			q.dst[k] = v
		}
		rows.Close()
	}
	return m, nil
}

func (s *Store) PutAssetAlias(ctx context.Context, uid, pattern, assetID string, at int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO asset_aliases (uid, pattern, asset_id, hits, updated_at) VALUES (?, ?, ?, 1, ?)
		ON CONFLICT(uid, pattern) DO UPDATE SET asset_id = excluded.asset_id, hits = hits + 1, updated_at = excluded.updated_at`,
		uid, pattern, assetID, at)
	return err
}

func (s *Store) PutMerchantRule(ctx context.Context, uid, merchant, categoryID string, at int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO merchant_rules (uid, merchant, category_id, hits, updated_at) VALUES (?, ?, ?, 1, ?)
		ON CONFLICT(uid, merchant) DO UPDATE SET category_id = excluded.category_id, hits = hits + 1, updated_at = excluded.updated_at`,
		uid, merchant, categoryID, at)
	return err
}
