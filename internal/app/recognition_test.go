package app

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/openrouter"
	"ledger-snap/internal/recognize"
)

type extractionStub struct{ facts []recognize.Fact }

func (f extractionStub) ChatJSON(_ context.Context, _ openrouter.ChatRequest, out any) (*openrouter.ChatResponse, error) {
	b, _ := json.Marshal(recognize.Extraction{Transactions: f.facts})
	return &openrouter.ChatResponse{}, json.Unmarshal(b, out)
}

type matchingStub struct{ allConfident bool }

func (m matchingStub) Decide(_ context.Context, req openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error) {
	answers := map[string]openrouter.Answer{}
	for q := range req.Questions {
		key, p := "a201", .99
		if strings.HasSuffix(q, "_category") {
			key = "c101"
		}
		if q == "b1_category" && !m.allConfident {
			key, p = "c102", .2
		}
		answers[q] = openrouter.Answer{Type: "choice", Choice: key, Probabilities: map[string]float64{key: p}}
	}
	return &openrouter.DecisionsResponse{Answers: answers}, nil
}
func setupRecognition(t *testing.T) (*Service, []byte, *fakeQianji) {
	s, _, fake := newTestService(t)
	s.Config.Server.DataDir = t.TempDir()
	s.Config.Extraction = config.Extraction{Model: "test", MaxTokens: 1000, ImageMaxSide: 1568, JPEGQuality: 85}
	s.Config.Recognize.ConfirmUncertain = true
	s.Config.Recognize.StaleScreenshot.Duration = 24 * time.Hour
	s.Config.Decision.Thresholds = config.Thresholds{Category: .4, Asset: .4}
	s.Recognizer = &recognize.Recognizer{Config: s.Config, Router: extractionStub{facts: []recognize.Fact{
		{Kind: recognize.KindExpense, Amount: "10.00", AmountRole: "paid", Currency: "CNY", Merchant: "第一笔", Payment: "招行"},
		{Kind: recognize.KindExpense, Amount: "20.00", AmountRole: "paid", Currency: "CNY", Merchant: "第二笔", Payment: "招行"},
	}}, Decider: matchingStub{}}
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 20, 40)))
	return s, b.Bytes(), fake
}

func TestRecognitionBatchConfirmEditDelete(t *testing.T) {
	s, image, _ := setupRecognition(t)
	ctx := context.Background()
	u, err := s.Store.User(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Recognize(ctx, u, image, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 2 || result.Records[0].Status != "written" || result.Records[1].Status != "needs_confirm" {
		t.Fatalf("result=%+v", result)
	}
	first, second := result.Records[0].RecordID, result.Records[1].RecordID
	if _, err := s.Confirm(ctx, u, second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, u, first, Patch{Money: "12.00"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(ctx, u, second); err != nil {
		t.Fatal(err)
	}
	current, err := s.BatchCurrent(ctx, u, result.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Records[0].Money != "12.00" || current.Records[0].Status != "written" || current.Records[1].Status != "deleted" {
		t.Fatalf("independent operations: %+v %+v", current.Records[0], current.Records[1])
	}
	if _, err := s.Confirm(ctx, u, first); err == nil {
		t.Fatal("written bill confirmed twice")
	}
}

func TestRecognitionNoTransactions(t *testing.T) {
	s, image, _ := setupRecognition(t)
	s.Recognizer.Router = extractionStub{facts: []recognize.Fact{}}
	ctx := context.Background()
	u, _ := s.Store.User(ctx, "u1")
	res, err := s.Recognize(ctx, u, image, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reread, err := s.BatchCurrent(ctx, u, res.BatchID)
	if err != nil || len(reread.Records) != 0 {
		t.Fatalf("empty batch: %+v %v", reread, err)
	}
}

func TestBatchWriteFailureKeepsOtherBillsAndRetryID(t *testing.T) {
	s, image, f := setupRecognition(t)
	ctx := context.Background()
	u, _ := s.Store.User(ctx, "u1")
	f.failMoney = "20.00"
	s.Recognizer.Decider = matchingStub{allConfident: true}
	stub := s.Recognizer.Router.(extractionStub)
	stub.facts = append(stub.facts, recognize.Fact{Kind: recognize.KindExpense, Amount: "30.00", AmountRole: "paid", Currency: "CNY", Merchant: "第三笔", Payment: "招行"})
	s.Recognizer.Router = stub
	result, err := s.Recognize(ctx, u, image, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 3 || result.Records[0].Status != "written" || result.Records[1].Status != "needs_confirm" || result.Records[2].Status != "written" {
		t.Fatalf("partial result: %+v", result.Records)
	}
	id := result.Records[1].RecordID
	before, err := s.Store.Record(ctx, u.UID, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.BillID == "" || result.Records[1].Reason == "" {
		t.Fatal("failed bill has no retry ID or explanation")
	}
	f.failMoney = ""
	if _, err := s.Confirm(ctx, u, id); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Store.Record(ctx, u.UID, id)
	if before.BillID != after.BillID {
		t.Fatal("retry created a new remote bill ID")
	}
	batch, err := s.BatchCurrent(ctx, u, result.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range batch.Records {
		if record.Status != "written" {
			t.Fatalf("bill not recovered: %+v", record)
		}
	}
}

func TestTypeChangeFailurePreservesWrittenBill(t *testing.T) {
	for _, fromTransfer := range []bool{false, true} {
		name := "expense to transfer"
		if fromTransfer {
			name = "transfer to expense"
		}
		t.Run(name, func(t *testing.T) {
			s, u, f := newTestService(t)
			ctx := context.Background()
			draft := lunch()
			patch := Patch{Kind: "转账", Asset: "招行", TargetAsset: "微信"}
			if fromTransfer {
				draft.Kind, draft.CategoryID, draft.TargetID = recognize.KindTransfer, "", "202"
				patch = Patch{Kind: "支出", Asset: "招行", Category: "餐饮"}
			}
			rec := written(t, s, u, draft)
			f.failMoney = "35.00"
			if _, err := s.Update(ctx, u, rec.ID, patch); err == nil {
				t.Fatal("expected the replacement write to fail")
			}
			current, err := s.Store.Record(ctx, u.UID, rec.ID)
			if err != nil || current.Status != "written" || current.BillID != rec.BillID || current.Draft != rec.Draft {
				t.Fatalf("failed replacement changed original record: %+v %v", current, err)
			}
			f.failMoney = ""
			if _, err := s.Update(ctx, u, rec.ID, Patch{Money: "36"}); err != nil {
				t.Fatalf("original bill is no longer editable: %v", err)
			}
			if _, err := s.Update(ctx, u, rec.ID, patch); err != nil {
				t.Fatalf("could not change type after recovery: %v", err)
			}
			current, _ = s.Store.Record(ctx, u.UID, rec.ID)
			if current.BillID == rec.BillID {
				t.Fatal("successful type switch kept the original ID")
			}
			if _, err := s.Delete(ctx, u, rec.ID); err != nil {
				t.Fatalf("replacement bill could not be deleted: %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.bills) != 0 {
				t.Fatalf("left bills behind: %v", f.bills)
			}
		})
	}
}

func TestDiscardAfterUnconfirmedWrite(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		name := "rejected write"
		if accepted {
			name = "accepted write with lost response"
		}
		t.Run(name, func(t *testing.T) {
			s, image, f := setupRecognition(t)
			s.Recognizer.Decider = matchingStub{allConfident: true}
			ctx := context.Background()
			u, _ := s.Store.User(ctx, "u1")
			if accepted {
				f.failAfterWrite = true
			} else {
				f.failMoney = "10.00"
			}
			batch, err := s.Recognize(ctx, u, image, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			first := batch.Records[0]
			if first.Status != "needs_confirm" || !first.WriteUnconfirmed {
				t.Fatalf("unconfirmed write not reported: %+v", first)
			}
			f.failAfterWrite, f.failMoney = false, ""
			rec, _ := s.Store.Record(ctx, u.UID, first.RecordID)
			deleted, err := s.Delete(ctx, u, rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := "discarded"
			if accepted {
				want = "deleted"
			}
			if deleted.Status != want {
				t.Fatalf("status = %s, want %s", deleted.Status, want)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, exists := f.bills[rec.BillID]; exists {
				t.Fatal("discarded record left a bill in Qianji")
			}
		})
	}
}
