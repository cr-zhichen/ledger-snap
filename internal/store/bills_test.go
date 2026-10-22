package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func billIDs(t *testing.T, s *Store, uid string) []string {
	t.Helper()
	bills, err := s.BillsBetween(context.Background(), uid, 0, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range bills {
		ids = append(ids, b.ID)
	}
	return ids
}

func TestApplySync(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	st, err := s.SyncState(ctx, "u1")
	if err != nil || st.Initialized() {
		t.Fatalf("state = %+v, %v", st, err)
	}
	full := []Bill{{ID: "1", Time: 10, Raw: `{"id":1}`}, {ID: "2", Time: 20, Raw: `{"id":2}`}, {ID: "3", Time: 30, Raw: `{"id":3}`}}
	if err := s.ApplySync(ctx, "u1", st, `{"t":1}`, full, nil, true, 100); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.SyncState(ctx, "u1"); !st.Initialized() || st.LastTimes != `{"t":1}` || st.Revision != 1 || st.SyncedAt != 100 {
		t.Fatalf("state = %+v", st)
	}
	// A delete wins over a change to the same bill in one sync.
	changes := []Bill{{ID: "2", Time: 25, Raw: `{"id":2,"v":2}`}, {ID: "3", Time: 30, Raw: `{"id":3,"v":2}`}}
	if err := s.ApplySync(ctx, "u1", st, `{"t":2}`, changes, []string{"1", "3"}, false, 200); err != nil {
		t.Fatal(err)
	}
	if ids := billIDs(t, s, "u1"); len(ids) != 1 || ids[0] != "2" {
		t.Fatalf("ids = %v", ids)
	}
	if b, err := s.Bill(ctx, "u1", "2"); err != nil || b.Raw != `{"id":2,"v":2}` || b.Time != 25 {
		t.Fatalf("bill = %+v, %v", b, err)
	}
	if _, err := s.Bill(ctx, "u1", "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted bill: %v", err)
	}
	// A full sync replaces what is there.
	st, _ = s.SyncState(ctx, "u1")
	if err := s.ApplySync(ctx, "u1", st, `{"t":3}`, []Bill{{ID: "9", Time: 5, Raw: `{}`}}, nil, true, 300); err != nil {
		t.Fatal(err)
	}
	if ids := billIDs(t, s, "u1"); len(ids) != 1 || ids[0] != "9" {
		t.Fatalf("ids after full = %v", ids)
	}
	if ids := billIDs(t, s, "u2"); len(ids) != 0 {
		t.Fatalf("other user sees %v", ids)
	}
}

func TestPutBillsStopsStaleSync(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	before, _ := s.SyncState(ctx, "u1")
	// This service writes a bill while a sync is pulling.
	if err := s.PutBills(ctx, "u1", []Bill{{ID: "1", Time: 10, Raw: `{"v":"new"}`}}, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.SyncState(ctx, "u1"); st.Initialized() || st.Revision != 1 {
		t.Fatalf("state = %+v", st)
	}
	err := s.ApplySync(ctx, "u1", before, `{"t":1}`, []Bill{{ID: "1", Time: 10, Raw: `{"v":"old"}`}}, nil, true, 100)
	if !errors.Is(err, ErrSyncConflict) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := s.Bill(ctx, "u1", "1"); b == nil || b.Raw != `{"v":"new"}` {
		t.Fatalf("bill = %+v", b)
	}
	if err := s.PutBills(ctx, "u1", nil, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if ids := billIDs(t, s, "u1"); len(ids) != 0 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestBillsBetween(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	st, _ := s.SyncState(ctx, "u1")
	bills := []Bill{{ID: "1", Time: 10, Raw: `{}`}, {ID: "2", Time: 20, Raw: `{}`}, {ID: "3", Time: 30, Raw: `{}`}}
	if err := s.ApplySync(ctx, "u1", st, `{}`, bills, nil, true, 1); err != nil {
		t.Fatal(err)
	}
	got, err := s.BillsBetween(ctx, "u1", 10, 20)
	if err != nil || len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if n, err := s.CountBills(ctx, "u1"); err != nil || n != 3 {
		t.Fatalf("count = %d, %v", n, err)
	}
}
