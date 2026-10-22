package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Bill is a cached Qianji bill.
type Bill struct {
	ID   string
	Time int64 // Unix seconds
	Raw  string
}

// SyncState is where a user's bill cache stands. Revision grows with every
// change to the cache, so a sync can tell whether the cache changed while it
// was pulling.
type SyncState struct {
	Revision  int64
	LastTimes string // where the next incremental sync starts; "" before the first full sync
	SyncedAt  int64
}

func (st SyncState) Initialized() bool { return st.LastTimes != "" }

// ErrSyncConflict means the bill cache changed while a sync was pulling, so
// the sync was not stored.
var ErrSyncConflict = errors.New("同步期间账单缓存有变化")

func (s *Store) SyncState(ctx context.Context, uid string) (SyncState, error) {
	var st SyncState
	err := s.db.QueryRowContext(ctx, `SELECT revision, lasttimes, synced_at FROM bill_sync WHERE uid = ?`, uid).Scan(&st.Revision, &st.LastTimes, &st.SyncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncState{}, nil
	}
	return st, err
}

// ApplySync stores a complete sync: its changes, then its deletes, which win
// over a change to the same bill. A full sync replaces the cache. Nothing is
// stored if the cache changed since before was read.
func (s *Store) ApplySync(ctx context.Context, uid string, before SyncState, lastTimes string, changes []Bill, deletes []string, full bool, at int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM bill_sync WHERE uid = ?`, uid).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if revision != before.Revision {
		return ErrSyncConflict
	}
	if full {
		if _, err := tx.ExecContext(ctx, `DELETE FROM bills WHERE uid = ?`, uid); err != nil {
			return err
		}
	}
	if err := putBills(ctx, tx, uid, changes, deletes); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bill_sync (uid, revision, lasttimes, synced_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(uid) DO UPDATE SET revision = excluded.revision, lasttimes = excluded.lasttimes, synced_at = excluded.synced_at`,
		uid, revision+1, lastTimes, at)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// PutBills records bills this service just wrote or deleted, ahead of the
// sync that will bring them. It counts as a change to the cache, so a sync
// already pulling, which may hold an older copy, is not stored.
func (s *Store) PutBills(ctx context.Context, uid string, put []Bill, drop []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := putBills(ctx, tx, uid, put, drop); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bill_sync (uid, revision) VALUES (?, 1)
		ON CONFLICT(uid) DO UPDATE SET revision = revision + 1`, uid)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func putBills(ctx context.Context, tx *sql.Tx, uid string, put []Bill, drop []string) error {
	for _, b := range put {
		_, err := tx.ExecContext(ctx, `INSERT INTO bills (uid, id, occurred_at, raw) VALUES (?, ?, ?, ?)
			ON CONFLICT(uid, id) DO UPDATE SET occurred_at = excluded.occurred_at, raw = excluded.raw`, uid, b.ID, b.Time, b.Raw)
		if err != nil {
			return err
		}
	}
	for _, id := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM bills WHERE uid = ? AND id = ?`, uid, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Bill(ctx context.Context, uid, id string) (*Bill, error) {
	b := Bill{ID: id}
	err := s.db.QueryRowContext(ctx, `SELECT occurred_at, raw FROM bills WHERE uid = ? AND id = ?`, uid, id).Scan(&b.Time, &b.Raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

// BillsBetween lists cached bills that happened in [from, to].
func (s *Store) BillsBetween(ctx context.Context, uid string, from, to int64) ([]Bill, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, occurred_at, raw FROM bills WHERE uid = ? AND occurred_at BETWEEN ? AND ? ORDER BY occurred_at`, uid, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bill
	for rows.Next() {
		var b Bill
		if err := rows.Scan(&b.ID, &b.Time, &b.Raw); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BillsByRemark lists, for each remark, the newest cached bills with exactly
// that remark that happened since a time, at most limit each.
func (s *Store) BillsByRemark(ctx context.Context, uid string, remarks []string, since int64, limit int) (map[string][]Bill, error) {
	out := map[string][]Bill{}
	if len(remarks) == 0 {
		return out, nil
	}
	args := []any{uid, since}
	for _, r := range remarks {
		args = append(args, r)
	}
	// A bill that is not valid JSON has no remark rather than failing the query.
	rows, err := s.db.QueryContext(ctx, `SELECT remark, id, occurred_at, raw FROM (
			SELECT CASE WHEN json_valid(raw) THEN json_extract(raw, '$.remark') END AS remark, id, occurred_at, raw
			FROM bills WHERE uid = ? AND occurred_at >= ?)
		WHERE remark IN (?`+strings.Repeat(", ?", len(remarks)-1)+`) ORDER BY occurred_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var remark string
		var b Bill
		if err := rows.Scan(&remark, &b.ID, &b.Time, &b.Raw); err != nil {
			return nil, err
		}
		if len(out[remark]) < limit {
			out[remark] = append(out[remark], b)
		}
	}
	return out, rows.Err()
}

// CountBills reports how many bills the user's cache holds.
func (s *Store) CountBills(ctx context.Context, uid string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM bills WHERE uid = ?`, uid).Scan(&n)
	return n, err
}
