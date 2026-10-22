package recognize

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/ocr"
	"ledger-snap/internal/openrouter"
	"ledger-snap/internal/qianji"
)

// beijing is the zone the config uses, whatever zone the tests run in.
var beijing = time.FixedZone("CST", 8*3600)

type fakeOCR struct{}

func (fakeOCR) Name() string                                          { return "fake" }
func (fakeOCR) Recognize(context.Context, []byte) ([]ocr.Line, error) { return nil, nil }

// fakeDecider answers like a confident model that files the bill under
// category with the given probability.
type fakeDecider struct {
	category string
	prob     float64
	req      openrouter.DecisionsRequest
}

func choice(key string, p float64) openrouter.Answer {
	return openrouter.Answer{Type: "choice", Choice: key, Probabilities: map[string]float64{key: p}}
}

// optionWith finds the option whose description contains text.
func optionWith(q openrouter.Question, text string) string {
	for key, desc := range q.Criteria.(map[string]any) {
		if strings.Contains(fmt.Sprint(desc), text) {
			return key
		}
	}
	return ""
}

func (f *fakeDecider) Decide(_ context.Context, req openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error) {
	f.req = req
	return &openrouter.DecisionsResponse{Answers: map[string]openrouter.Answer{
		"transaction":      {Type: "noul", Noul: 0.99},
		"kind":             choice(string(KindExpense), 0.99),
		"amount":           choice(optionWith(req.Questions["amount"], "28.00"), 0.95),
		"time":             choice("none", 0.9),
		"category_expense": choice("c"+f.category, f.prob),
		"asset":            choice("s201", 0.9),
		"merchant":         choice(optionWith(req.Questions["merchant"], "美团"), 0.9),
	}}, nil
}

func (f *fakeDecider) stateText() string {
	var parts []string
	for _, s := range f.req.State {
		if text, ok := s.(string); ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

var historyCatalog = qianji.Catalog{
	Categories: []qianji.Category{
		{ID: "101", Name: "外卖", Path: "餐饮/外卖", Type: qianji.TypeExpense, Leaf: true},
		{ID: "102", Name: "买菜", Path: "餐饮/买菜", Type: qianji.TypeExpense, Leaf: true},
		{ID: "103", Name: "工资", Path: "工资", Type: qianji.TypeIncome, Leaf: true},
	},
	Assets: []qianji.Asset{{ID: "201", Name: "招行"}},
}

// recognizeWith runs a meituan payment through the pipeline with the given
// earlier bills of 美团.
func recognizeWith(t *testing.T, past []PastBill, d *fakeDecider) (*Draft, *Trace, []string) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Recognize.Timezone = "Asia/Shanghai"
	cfg.Recognize.StaleScreenshot.Duration = 24 * time.Hour
	cfg.Decision.MaxCandidates = 40
	cfg.Decision.Thresholds = config.Thresholds{Transaction: 0.5, Type: 0.5, Amount: 0.5, Time: 0.5, Category: 0.5, Asset: 0.5}
	r := &Recognizer{Config: cfg, OCR: fakeOCR{}, Decider: d}
	var asked []string
	in := Input{
		UploadTime: time.Date(2026, 10, 22, 12, 30, 0, 0, beijing),
		Catalog:    historyCatalog,
		Lines: []ocr.Line{
			{Text: "支付成功", Y: 100, H: 30},
			{Text: "-28.00", Y: 200, H: 30},
			{Text: "交易商户：美团", Y: 300, H: 30},
		},
		History: func(_ context.Context, names []string) (map[string][]PastBill, error) {
			asked = names
			if past == nil {
				return map[string][]PastBill{}, nil
			}
			return map[string][]PastBill{"美团": past}, nil
		},
	}
	draft, trace, err := r.Recognize(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Status != "ok" || draft.Merchant != "美团" || draft.Money != "28.00" {
		t.Fatalf("draft = %+v", draft)
	}
	return draft, trace, asked
}

func past(kind Kind, category string, daysAgo int) PastBill {
	return PastBill{Time: time.Date(2026, 10, 22-daysAgo, 18, 40, 0, 0, beijing), Kind: kind, Money: "35.50", CategoryID: category, AssetID: "201"}
}

func TestHistoryAgreeing(t *testing.T) {
	d := &fakeDecider{category: "101", prob: 0.9}
	draft, trace, asked := recognizeWith(t, []PastBill{past(KindExpense, "102", 1), past(KindExpense, "102", 3)}, d)
	if !slices.Contains(asked, "美团") || !slices.Contains(asked, "支付成功") {
		t.Errorf("looked up %v", asked)
	}
	// Two bills, both 买菜: the model's 外卖 gives way.
	if draft.CategoryID != "102" || slices.Contains(draft.Uncertain, "category") || !slices.Contains(trace.MemoryHits, "category") {
		t.Errorf("draft = %+v, hits = %v", draft, trace.MemoryHits)
	}
	state := d.stateText()
	if !strings.Contains(state, "「美团」") || !strings.Contains(state, "2026-10-21 周三 18:40 · 支出 35.50 · 招行 → 餐饮/买菜") {
		t.Errorf("state = %s", state)
	}
	if q := fmt.Sprint(d.req.Questions["category_expense"].Instructions); !strings.Contains(q, "以用户的习惯为准") {
		t.Errorf("category question = %s", q)
	}
	if len(trace.History["美团"]) != 2 {
		t.Errorf("trace history = %+v", trace.History)
	}
}

func TestHistoryLeftToModel(t *testing.T) {
	cases := map[string][]PastBill{
		"mixed":         {past(KindExpense, "101", 1), past(KindExpense, "102", 2)},
		"one":           {past(KindExpense, "102", 1)},
		"other kind":    {past(KindIncome, "103", 1), past(KindIncome, "103", 2), past(KindExpense, "102", 3)},
		"latest differ": {past(KindExpense, "101", 1), past(KindExpense, "102", 2), past(KindExpense, "102", 3)},
	}
	for name, bills := range cases {
		t.Run(name, func(t *testing.T) {
			d := &fakeDecider{category: "101", prob: 0.4}
			draft, trace, _ := recognizeWith(t, bills, d)
			// The model decides with the history in front of it, and its low
			// confidence still asks the user.
			if draft.CategoryID != "101" || !slices.Contains(draft.Uncertain, "category") || slices.Contains(trace.MemoryHits, "category") {
				t.Errorf("draft = %+v, hits = %v", draft, trace.MemoryHits)
			}
			if !strings.Contains(d.stateText(), "「美团」") {
				t.Error("history not shown to the model")
			}
		})
	}
}

func TestNoHistory(t *testing.T) {
	d := &fakeDecider{category: "101", prob: 0.9}
	draft, trace, _ := recognizeWith(t, nil, d)
	if draft.CategoryID != "101" || len(trace.History) != 0 || strings.Contains(d.stateText(), "以前") {
		t.Errorf("draft = %+v, state = %s", draft, d.stateText())
	}
	if q := fmt.Sprint(d.req.Questions["category_expense"].Instructions); strings.Contains(q, "参考") {
		t.Errorf("category question = %s", q)
	}
}

func TestHistoryCategory(t *testing.T) {
	five := []PastBill{past(KindExpense, "102", 1), past(KindExpense, "102", 2), past(KindExpense, "102", 3), past(KindExpense, "102", 4), past(KindExpense, "102", 5)}
	// Only the latest five count, so an old exception does not block.
	if id, ok := historyCategory(append(five, past(KindExpense, "101", 6)), KindExpense); !ok || id != "102" {
		t.Errorf("got %q, %v", id, ok)
	}
	if _, ok := historyCategory(five, KindIncome); ok {
		t.Error("income should not use expense history")
	}
}
