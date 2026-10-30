package store

import (
	"context"
	"database/sql"
	"errors"
)

type Batch struct {
	ID, UID, Reason string
	CreatedAt       int64
}

// SaveBatch persists every draft before any external bill write is attempted.
func (s *Store) SaveBatch(ctx context.Context, b *Batch, records []*Record) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO recognition_batches(id,uid,created_at,reason) VALUES(?,?,?,?)`, b.ID, b.UID, b.CreatedAt, b.Reason); err != nil {
		return err
	}
	for i, r := range records {
		if err = saveRecord(ctx, tx, r); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO recognition_items(batch_id,record_id,position) VALUES(?,?,?)`, b.ID, r.ID, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Batch(ctx context.Context, uid, id string) (*Batch, []*Record, error) {
	var b Batch
	err := s.db.QueryRowContext(ctx, `SELECT id,uid,created_at,reason FROM recognition_batches WHERE uid=? AND id=?`, uid, id).Scan(&b.ID, &b.UID, &b.CreatedAt, &b.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordColumns+` FROM records WHERE uid=? AND id IN (SELECT record_id FROM recognition_items WHERE batch_id=?) ORDER BY (SELECT position FROM recognition_items WHERE record_id=records.id)`, uid, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var records []*Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, nil, err
		}
		records = append(records, r)
	}
	return &b, records, rows.Err()
}
