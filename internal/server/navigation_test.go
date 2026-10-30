package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledger-snap/internal/app"
	"ledger-snap/internal/config"
	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
	"ledger-snap/internal/store"
)

func navigationServer(t *testing.T) (*Server, *store.User, func(string, string) *Step) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	catalog := qianji.Catalog{
		Assets:     []qianji.Asset{{ID: "1", Name: "招行"}, {ID: "2", Name: "微信"}},
		Categories: []qianji.Category{{ID: "1", Path: "餐饮", Type: qianji.TypeExpense, Leaf: true}, {ID: "2", Path: "工资", Type: qianji.TypeIncome, Leaf: true}},
		Prices:     map[string]float64{"CNY": 1},
	}
	raw, _ := json.Marshal(catalog)
	u := &store.User{UID: "u", Account: "test", Catalog: string(raw), CatalogAt: time.Now().Unix()}
	if err := db.SaveUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	var records []*store.Record
	for i, kind := range []recognize.Kind{recognize.KindExpense, recognize.KindTransfer} {
		d := recognize.Draft{Status: "ok", Kind: kind, Money: "50.68", CategoryID: "1", AssetID: "1", TargetID: "2", Time: time.Now(), Merchant: "测试商户"}
		b, _ := json.Marshal(d)
		records = append(records, &store.Record{ID: []string{"r1", "r2"}[i], UID: u.UID, Status: "needs_confirm", Draft: string(b), Summary: "原账单"})
	}
	if err := db.SaveBatch(context.Background(), &store.Batch{ID: "batch", UID: u.UID}, records); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Qianji.CatalogTTL.Duration = time.Hour
	cfg.Recognize.Timezone = "Asia/Shanghai"
	s := &Server{App: &app.Service{Store: db, Config: cfg}}
	post := func(path, value string) *Step {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"value": value})
		r := httptest.NewRequest("POST", path, bytes.NewReader(body))
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v1/flow/"), "/", 2)
		r.SetPathValue("id", parts[0])
		r.SetPathValue("step", parts[1])
		w := httptest.NewRecorder()
		s.flowStep(w, r, u)
		var step Step
		if err := json.Unmarshal(w.Body.Bytes(), &step); err != nil {
			t.Fatal(err)
		}
		return &step
	}
	return s, u, post
}

func TestEditNavigationKeepsDraftUntilFinalSelection(t *testing.T) {
	s, u, post := navigationServer(t)
	before, _ := s.App.Store.Record(context.Background(), u.UID, "r1")
	next := "/v1/flow/r1/main?batch=batch"
	for _, tc := range []struct{ value, path, selected string }{
		{optEdit, "edit", ""}, {optKind, "kind", ""},
		{"转账", "source/transfer", ""}, {"招行", "target/transfer", "招行"},
		{optBack, "source/transfer", "招行"}, {optBack, "kind", "转账"},
		{optBack, "edit", ""}, {optKind, "kind", "转账"}, {"转账", "source/transfer", "招行"}, {optBack, "kind", "转账"},
		{"收入", "category/income", ""}, {optBack, "kind", "收入"},
		{optBack, "edit", ""}, {optAsset, "asset", ""}, {optBack, "edit", ""},
		{optMoney, "money", ""}, {"", "edit", ""}, {optBack, "main", ""},
	} {
		step := post(next, tc.value)
		parsed, _ := url.Parse(step.Next)
		if parsed.Path != "/v1/flow/r1/"+tc.path || parsed.Query().Get("batch") != "batch" {
			t.Fatalf("%s + %q => %+v", next, tc.value, step)
		}
		if tc.selected != "" && !strings.Contains(step.Prompt, tc.selected) {
			t.Fatalf("lost pending selection %q: %+v", tc.selected, step)
		}
		next = step.Next
	}
	step := post(next, optBatch)
	if step.Next != "/v1/flow/batch/batch" {
		t.Fatalf("did not return to batch: %+v", step)
	}
	after, _ := s.App.Store.Record(context.Background(), u.UID, "r1")
	if *before != *after {
		t.Fatal("navigation saved an incomplete edit")
	}
}

func TestTransferAccountBackReturnsToItsEntry(t *testing.T) {
	_, _, post := navigationServer(t)
	step := post("/v1/flow/r2/edit?batch=batch", optAsset)
	step = post(step.Next, "招行")
	step = post(step.Next, optBack)
	if !strings.Contains(step.Prompt, "招行") {
		t.Fatal("lost selected source account")
	}
	step = post(step.Next, optBack)
	parsed, _ := url.Parse(step.Next)
	if parsed.Path != "/v1/flow/r2/edit" || parsed.Query().Get("batch") != "batch" {
		t.Fatalf("account edit returned to type picker: %+v", step)
	}
}

func TestFinishFromBatchRecordEndsFlow(t *testing.T) {
	s, u, post := navigationServer(t)
	record, _ := s.App.Store.Record(context.Background(), u.UID, "r1")
	record.Status = "written"
	if err := s.App.Store.SaveRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	step := post("/v1/flow/r1/main?batch=batch", optDone)
	if step.Action != "done" || step.Next != "" {
		t.Fatalf("finish unexpectedly reopened batch: %+v", step)
	}
}

func TestFinalTransferSelectionSavesTogether(t *testing.T) {
	s, u, post := navigationServer(t)
	step := post("/v1/flow/r1/edit?batch=batch", optKind)
	step = post(step.Next, "转账")
	step = post(step.Next, "招行")
	step = post(step.Next, "微信")
	parsed, _ := url.Parse(step.Next)
	if parsed.Path != "/v1/flow/r1/main" || parsed.Query().Get("batch") != "batch" {
		t.Fatalf("edit did not return to bill details: %+v", step)
	}
	record, err := s.App.Store.Record(context.Background(), u.UID, "r1")
	if err != nil {
		t.Fatal(err)
	}
	var draft recognize.Draft
	if err := json.Unmarshal([]byte(record.Draft), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Kind != recognize.KindTransfer || draft.AssetID != "1" || draft.TargetID != "2" || draft.CategoryID != "" || draft.Money != "50.68" || record.Status != "needs_confirm" {
		t.Fatalf("incomplete or unintended change: %+v, status %s", draft, record.Status)
	}
}
