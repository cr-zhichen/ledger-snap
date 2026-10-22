package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
	"ledger-snap/internal/store"
)

// fakeQianji keeps bills the way the server would and answers the two sync
// routes. Every pull returns all bills and every deletion so far, which is
// what an incremental pull returns when everything changed.
type fakeQianji struct {
	mu      sync.Mutex
	bills   map[string]map[string]any
	deleted []json.Number
	writes  []map[string]any // bills sent to /bill/syncall
	pulls   int
	onPull  func(n int) // runs before pull n answers
}

func (f *fakeQianji) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	reply := func(data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ec": 200, "em": "", "data": data})
	}
	switch r.URL.Path {
	case "/syncv2/pull":
		f.mu.Lock()
		f.pulls++
		n, hook := f.pulls, f.onPull
		f.mu.Unlock()
		if hook != nil {
			hook(n)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		changes := []any{}
		for _, b := range f.bills {
			changes = append(changes, b)
		}
		reply(map[string]any{"changes": changes, "deletes": append([]json.Number{}, f.deleted...), "bookid": -1,
			"pageoffset": 0, "pagesign": "", "hasmore": 0, "lasttimes": map[string]int{"n": n}})
	case "/bill/syncall":
		var v struct {
			Bills struct {
				Changes []map[string]any `json:"changelist"`
				Deletes []json.Number    `json:"dellist"`
			} `json:"bills"`
		}
		dec := json.NewDecoder(strings.NewReader(r.Form.Get("v")))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		created, deleted := []json.Number{}, []json.Number{}
		for _, id := range v.Bills.Deletes {
			if _, ok := f.bills[id.String()]; ok {
				delete(f.bills, id.String())
				f.deleted = append(f.deleted, id)
				deleted = append(deleted, id)
			}
		}
		for _, b := range v.Bills.Changes {
			id := fmt.Sprint(b["id"])
			f.bills[id] = b
			f.writes = append(f.writes, b)
			created = append(created, json.Number(id))
		}
		reply(map[string]any{"sync_result": map[string]any{"bill": map[string]any{
			"new_ids": created, "update_ids": []any{}, "del_ids": deleted, "conf_ids": []any{}, "has_failed": 0}}})
	default:
		http.Error(w, "unexpected "+r.URL.Path, 404)
	}
}

// edit changes a bill the way the app would.
func (f *fakeQianji) edit(id string, fields map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range fields {
		f.bills[id][k] = v
	}
}

func (f *fakeQianji) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.bills, id)
	f.deleted = append(f.deleted, json.Number(id))
}

func (f *fakeQianji) lastWrite() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes[len(f.writes)-1]
}

func (f *fakeQianji) counts() (writes, pulls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes), f.pulls
}

var testCatalog = qianji.Catalog{
	Categories: []qianji.Category{
		{ID: "101", Name: "餐饮", Path: "餐饮", Type: qianji.TypeExpense, Leaf: true},
		{ID: "102", Name: "购物", Path: "购物", Type: qianji.TypeExpense, Leaf: true},
	},
	Assets: []qianji.Asset{{ID: "201", Name: "招行", Type: 1}, {ID: "202", Name: "微信", Type: 1}},
	Prices: map[string]float64{"CNY": 1},
}

func newTestService(t *testing.T) (*Service, *store.User, *fakeQianji) {
	t.Helper()
	f := &fakeQianji{bills: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{}
	cfg.Qianji.SyncWait.Duration = 2 * time.Second
	cfg.Qianji.CatalogTTL.Duration = time.Hour
	cfg.Recognize.Timezone = "Asia/Shanghai"
	cfg.Recognize.DedupWindow.Duration = 5 * time.Minute
	now := time.Now().Unix()
	u := &store.User{UID: "u1", Account: "a", PasswordMD5: strings.Repeat("0", 32), QJToken: "t", Device: "d", CreatedAt: now,
		Catalog: toJSON(testCatalog), CatalogAt: now}
	ctx := context.Background()
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCatalog(ctx, u.UID, u.Catalog, u.CatalogAt); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Config: cfg, Store: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Qianji: qianji.New(qianji.Options{BaseURL: srv.URL, Timeout: 5 * time.Second, VersionCode: 1})}
	// Let background syncs finish before the database closes.
	t.Cleanup(func() { <-svc.startSync(u.UID, false).done })
	return svc, u, f
}

// written records a bill through the service: a held-back record, confirmed.
func written(t *testing.T, s *Service, u *store.User, d *recognize.Draft) *store.Record {
	t.Helper()
	ctx := context.Background()
	rec := &store.Record{ID: "r_" + randomHex(4), UID: u.UID, CreatedAt: time.Now().Unix(), Status: "needs_confirm", Draft: toJSON(d)}
	if err := s.Store.SaveRecord(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(ctx, u, rec.ID); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Store.Record(ctx, u.UID, rec.ID)
	if err != nil || rec.Status != "written" {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	return rec
}

func lunch() *recognize.Draft {
	return &recognize.Draft{Status: "ok", Kind: recognize.KindExpense, Money: "35.00", CategoryID: "101", AssetID: "201",
		Time: time.Date(2026, 10, 22, 12, 30, 0, 0, time.Local), Merchant: "麦当劳"}
}

func TestEditKeepsAppChanges(t *testing.T) {
	s, u, f := newTestService(t)
	rec := written(t, s, u, lunch())
	f.edit(rec.BillID, map[string]any{"cateid": json.Number("102"), "remark": "麦当劳（App 里改的）", "images": []any{"a.jpg"}})

	res, err := s.Update(context.Background(), u, rec.ID, Patch{Money: "40"})
	if err != nil {
		t.Fatal(err)
	}
	got := f.lastWrite()
	for k, want := range map[string]string{"id": rec.BillID, "money": "40.00", "cateid": "102", "assetid": "201", "remark": "麦当劳（App 里改的）", "images": "[a.jpg]"} {
		if v := fmt.Sprint(got[k]); v != want {
			t.Errorf("%s = %s, want %s", k, v, want)
		}
	}
	if !strings.Contains(res.Summary, "购物") || !strings.Contains(res.Summary, "40.00") || !strings.Contains(res.Summary, "App 里改的") {
		t.Errorf("summary = %s", res.Summary)
	}
	// The edit is in the cache before any sync brings it.
	b, err := s.Store.Bill(context.Background(), u.UID, rec.BillID)
	if err != nil || !strings.Contains(b.Raw, `"money":40.00`) {
		t.Errorf("cached = %+v, %v", b, err)
	}
}

func TestEditOfBillDeletedInApp(t *testing.T) {
	s, u, f := newTestService(t)
	rec := written(t, s, u, lunch())
	f.remove(rec.BillID)
	writes, _ := f.counts()

	_, err := s.Update(context.Background(), u, rec.ID, Patch{Money: "40"})
	var ue *UserError
	if !errors.As(err, &ue) || ue.Status != 409 {
		t.Fatalf("err = %v", err)
	}
	if after, _ := f.counts(); after != writes {
		t.Fatal("the bill was written again")
	}
	if cur, _ := s.Store.Record(context.Background(), u.UID, rec.ID); cur.Status != "deleted" {
		t.Fatalf("status = %s", cur.Status)
	}
}

func TestEditGivesUpOnSlowSync(t *testing.T) {
	s, u, f := newTestService(t)
	rec := written(t, s, u, lunch())
	s.Config.Qianji.SyncWait.Duration = 50 * time.Millisecond
	f.onPull = func(int) { time.Sleep(300 * time.Millisecond) }
	writes, _ := f.counts()

	_, err := s.Update(context.Background(), u, rec.ID, Patch{Money: "40"})
	var ue *UserError
	if !errors.As(err, &ue) || ue.Status != 504 {
		t.Fatalf("err = %v", err)
	}
	if after, _ := f.counts(); after != writes {
		t.Fatal("the bill was written without the latest copy")
	}
}

func TestDuplicateOfBillRecordedInApp(t *testing.T) {
	s, u, f := newTestService(t)
	ctx := context.Background()
	when := time.Date(2026, 10, 22, 12, 30, 0, 0, time.Local)
	f.bills["1767225600000100001"] = map[string]any{"id": json.Number("1767225600000100001"), "type": 0, "money": 35,
		"time": when.Add(2 * time.Minute).Unix(), "remark": "午饭", "cateid": json.Number("101"), "assetid": json.Number("201")}
	if _, err := s.SyncBills(ctx, u.UID, false); err != nil {
		t.Fatal(err)
	}
	d := lunch()
	m, _ := moneyOf(d, testCatalog)
	dup, err := s.duplicate(ctx, u, d, m, testCatalog)
	if err != nil || !strings.HasPrefix(dup, "钱迹里已有「支出 35.00 · 餐饮 · 招行") || !strings.Contains(dup, "午饭") {
		t.Fatalf("dup = %q, %v", dup, err)
	}
	d.AssetID = "202"
	if dup, _ := s.duplicate(ctx, u, d, m, testCatalog); dup != "" {
		t.Fatalf("other account: %q", dup)
	}
}

func TestDeleteOfBillDeletedInApp(t *testing.T) {
	s, u, f := newTestService(t)
	ctx := context.Background()
	rec := written(t, s, u, lunch())
	f.remove(rec.BillID)
	if _, err := s.SyncBills(ctx, u.UID, false); err != nil {
		t.Fatal(err)
	}
	res, err := s.Delete(ctx, u, rec.ID)
	if err != nil || res.Status != "deleted" {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

func TestSyncsCoalesce(t *testing.T) {
	s, u, f := newTestService(t)
	release := make(chan struct{})
	f.onPull = func(n int) {
		if n == 1 {
			<-release
		}
	}
	first := s.startSync(u.UID, false)
	if s.startSync(u.UID, false) != first {
		t.Fatal("a background sync did not join the running one")
	}
	fresh := s.startSync(u.UID, true)
	if fresh == first || s.startSync(u.UID, true) != fresh {
		t.Fatal("fresh syncs should share the run after the current one")
	}
	close(release)
	<-fresh.done
	if first.err != nil || fresh.err != nil {
		t.Fatal(first.err, fresh.err)
	}
	if _, pulls := f.counts(); pulls != 2 {
		t.Fatalf("pulls = %d", pulls)
	}
}

func TestSyncRetriesAfterOwnWrite(t *testing.T) {
	s, u, f := newTestService(t)
	ctx := context.Background()
	f.onPull = func(n int) {
		if n == 1 {
			// This service writes a bill while the first pull is under way.
			_ = s.Store.PutBills(ctx, u.UID, []store.Bill{{ID: "5", Time: 1, Raw: `{}`}}, nil)
		}
	}
	r, err := s.SyncBills(ctx, u.UID, false)
	if err != nil || !r.Full {
		t.Fatalf("report = %+v, %v", r, err)
	}
	if _, pulls := f.counts(); pulls != 2 {
		t.Fatalf("pulls = %d", pulls)
	}
	if st, _ := s.Store.SyncState(ctx, u.UID); !st.Initialized() || !bytes.Contains([]byte(st.LastTimes), []byte(`"n":2`)) {
		t.Fatalf("state = %+v", st)
	}
}
