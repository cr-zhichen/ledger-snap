package store

import (
	"context"
	"errors"
	"testing"
)

func TestBatchIsolationOrderAndUpdates(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	b := &Batch{ID: "batch", UID: "u1", CreatedAt: 1}
	records := []*Record{{ID: "z", UID: "u1", CreatedAt: 1, Status: "needs_confirm", Draft: `{"money":"50.68"}`}, {ID: "a", UID: "u1", CreatedAt: 1, Status: "needs_confirm", Draft: `{"money":"5.92"}`}}
	if err := s.SaveBatch(ctx, b, records); err != nil {
		t.Fatal(err)
	}
	records[1].Status = "written"
	records[1].BillID = "42"
	if err := s.SaveRecord(ctx, records[1]); err != nil {
		t.Fatal(err)
	}
	_, got, err := s.Batch(ctx, "u1", "batch")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "z" || got[1].ID != "a" || got[1].Status != "written" || got[1].BillID != "42" {
		t.Fatalf("batch = %+v", got)
	}
	if _, _, err := s.Batch(ctx, "u2", "batch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user accessed batch: %v", err)
	}
}

func TestBatchCreationIsAtomic(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	b := &Batch{ID: "b", UID: "u"}
	records := []*Record{{ID: "duplicate", UID: "u"}, {ID: "duplicate", UID: "u"}}
	if err := s.SaveBatch(ctx, b, records); err == nil {
		t.Fatal("duplicate member should fail")
	}
	if _, _, err := s.Batch(ctx, "u", "b"); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed batch persisted")
	}
	if _, err := s.Record(ctx, "u", "duplicate"); !errors.Is(err, ErrNotFound) {
		t.Fatal("draft escaped rolled-back batch")
	}
}
