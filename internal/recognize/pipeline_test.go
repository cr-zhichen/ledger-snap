package recognize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/openrouter"
	"ledger-snap/internal/qianji"
)

type chatFunc func(context.Context, openrouter.ChatRequest, any) (*openrouter.ChatResponse, error)

func (f chatFunc) ChatJSON(c context.Context, r openrouter.ChatRequest, v any) (*openrouter.ChatResponse, error) {
	return f(c, r, v)
}

type decisionFunc func(context.Context, openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error)

func (f decisionFunc) Decide(c context.Context, r openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error) {
	return f(c, r)
}
func answer(key string, p float64) openrouter.Answer {
	return openrouter.Answer{Type: "choice", Choice: key, Probabilities: map[string]float64{key: p}}
}
func configForTest() *config.Config {
	c := &config.Config{}
	c.Recognize.Timezone = "Asia/Shanghai"
	c.Recognize.StaleScreenshot.Duration = 24 * time.Hour
	c.Extraction = config.Extraction{Model: "vision", MaxTokens: 8000, ImageMaxSide: 1568, JPEGQuality: 85}
	c.Decision.Model = "jev"
	c.Decision.Thresholds = config.Thresholds{Category: .4, Asset: .4}
	return c
}
func testImage() []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 20, 40)))
	return b.Bytes()
}
func nowForTest() time.Time {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	return time.Date(2026, 10, 30, 21, 32, 0, 0, loc)
}
func decodeInto(v, out any) error { b, _ := json.Marshal(v); return json.Unmarshal(b, out) }

var catalogForTest = qianji.Catalog{
	Categories: []qianji.Category{{ID: "food", Path: "三餐", Type: qianji.TypeExpense, Leaf: true}, {ID: "other", Path: "其它", Type: qianji.TypeExpense, Leaf: true}},
	Assets:     []qianji.Asset{{ID: "credit", Name: "花呗", Type: qianji.AssetTypeCredit}, {ID: "balance", Name: "支付宝余额"}},
}

func TestBatchExtractionAndMatching(t *testing.T) {
	cfg := configForTest()
	r := &Recognizer{Config: cfg}
	r.Router = chatFunc(func(_ context.Context, req openrouter.ChatRequest, out any) (*openrouter.ChatResponse, error) {
		raw, _ := json.Marshal(req)
		if !bytes.Contains(raw, []byte("data:image/jpeg;base64,")) {
			t.Fatal("image not sent")
		}
		return &openrouter.ChatResponse{}, decodeInto(Extraction{Transactions: []Fact{
			{Kind: KindExpense, Amount: "50.68", AmountRole: "paid", Currency: "CNY", Clock: "17:37", Merchant: "超市", Payment: "花呗"},
			{Kind: KindExpense, Amount: "5.92", AmountRole: "paid", Currency: "CNY", Clock: "16:52", Merchant: "物流", Payment: "账户余额"},
			{Kind: KindExpense, Amount: "18.90", AmountRole: "paid", Currency: "CNY", Date: "yesterday", Clock: "20:02", Merchant: "闪购", Payment: "花呗"},
		}}, out)
	})
	var requests int
	r.Decider = decisionFunc(func(_ context.Context, req openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error) {
		requests++
		if len(req.Questions) != 6 {
			t.Fatalf("questions=%v", req.Questions)
		}
		raw, _ := json.Marshal(req)
		if bytes.Contains(raw, []byte("image_url")) {
			t.Fatal("matching should use extracted facts")
		}
		return &openrouter.DecisionsResponse{Answers: map[string]openrouter.Answer{
			"b0_category": answer("cfood", .9), "b0_asset": answer("acredit", .99),
			"b1_category": answer("cother", .9), "b1_asset": answer("abalance", .95),
			"b2_category": answer("cfood", .3), "b2_asset": answer("acredit", .99),
		}}, nil
	})
	var merchants []string
	in := Input{Image: testImage(), UploadTime: nowForTest(), Catalog: catalogForTest, History: func(_ context.Context, names []string) (map[string][]PastBill, error) {
		merchants = names
		return nil, nil
	}}
	ds, _, err := r.Recognize(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 || requests != 1 || strings.Join(merchants, ",") != "超市,物流,闪购" {
		t.Fatalf("batch/history: %+v %v", ds, merchants)
	}
	for i, want := range []struct{ money, asset, when string }{{"50.68", "credit", "2026-10-30 17:37"}, {"5.92", "balance", "2026-10-30 16:52"}, {"18.90", "credit", "2026-10-29 20:02"}} {
		if ds[i].Money != want.money || ds[i].AssetID != want.asset || ds[i].Time.Format("2006-01-02 15:04") != want.when {
			t.Errorf("item %d: %+v", i, ds[i])
		}
	}
	if !contains(ds[2].Uncertain, "category") {
		t.Fatal("low confidence category was not held")
	}
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func TestEmptyAndFailedExtraction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		output  Extraction
		err     error
		wantErr bool
	}{{"nontransaction", Extraction{Transactions: []Fact{}, Reason: "不是交易"}, nil, false}, {"bad response", Extraction{}, nil, true}, {"network", Extraction{}, errors.New("offline"), true}} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Recognizer{Config: configForTest(), Router: chatFunc(func(_ context.Context, _ openrouter.ChatRequest, out any) (*openrouter.ChatResponse, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &openrouter.ChatResponse{}, decodeInto(tc.output, out)
			})}
			r.Decider = decisionFunc(func(context.Context, openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error) {
				t.Fatal("empty/failed extraction reached matching")
				return nil, nil
			})
			ds, _, err := r.Recognize(context.Background(), Input{Image: testImage(), UploadTime: nowForTest()})
			if (err != nil) != tc.wantErr || len(ds) != 0 {
				t.Fatalf("drafts=%v err=%v", ds, err)
			}
		})
	}
}

func TestHistoryAndAliasesOverrideMatching(t *testing.T) {
	r := &Recognizer{Config: configForTest(), Decider: decisionFunc(func(_ context.Context, req openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error) {
		raw, _ := json.Marshal(req.State)
		if !bytes.Contains(raw, []byte("超市")) {
			t.Fatal("merchant history missing")
		}
		return &openrouter.DecisionsResponse{Answers: map[string]openrouter.Answer{"b0_category": answer("cfood", .2), "b0_asset": answer("acredit", .2)}}, nil
	})}
	d := &Draft{Status: "ok", Kind: KindExpense, Money: "20.00", Merchant: "超市", Payment: "指定付款方式"}
	in := Input{Catalog: catalogForTest, Memory: Memory{AssetAliases: map[string]string{"指定付款方式": "balance"}}, History: func(context.Context, []string) (map[string][]PastBill, error) {
		return map[string][]PastBill{"超市": {{Kind: KindExpense, CategoryID: "other"}, {Kind: KindExpense, CategoryID: "other"}}}, nil
	}}
	if err := r.Match(context.Background(), []*Draft{d}, in, &Trace{}); err != nil {
		t.Fatal(err)
	}
	if d.CategoryID != "other" || d.AssetID != "balance" || len(d.Uncertain) != 0 {
		t.Fatalf("memory not applied: %+v", d)
	}
}

func TestFinancialFacts(t *testing.T) {
	r := &Recognizer{Config: configForTest()}
	for _, tc := range []struct {
		name  string
		f     Fact
		money string
	}{
		{"withdraw principal plus fee", Fact{Kind: KindTransfer, Amount: "2000.00", AmountRole: "principal", Fee: "2.00"}, "2002.00"},
		{"withdraw shown arrival clarifies deduction", Fact{Kind: KindTransfer, Amount: "500.00", AmountRole: "principal", Received: "499.50", Fee: "0.50"}, "500.00"},
		{"withdraw paid includes fee", Fact{Kind: KindTransfer, Amount: "500.00", AmountRole: "paid", Fee: "0.50"}, "500.00"},
		{"withdraw only arrival", Fact{Kind: KindTransfer, Amount: "499.50", AmountRole: "received", Fee: "0.50"}, "500.00"},
		{"repayment discount", Fact{Kind: KindCredit, Amount: "1000.00", AmountRole: "received", Discount: "5.00"}, "995.00"},
		{"paid not discounted twice", Fact{Kind: KindExpense, Amount: "59.85", AmountRole: "paid", Discount: "40.05"}, "59.85"},
		{"currency conversion amounts", Fact{Kind: KindTransfer, Amount: "7881.80", AmountRole: "paid", Currency: "HKD", Converted: "1000.00", ConvertedCurrency: "USD"}, "7881.80"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := r.draft(tc.f, nowForTest())
			if err != nil {
				t.Fatal(err)
			}
			if d.Money != tc.money {
				t.Fatalf("got %s want %s", d.Money, tc.money)
			}
		})
	}
	for _, s := range []string{"، 5.00", "5abc", "-5.00", "NaN", "0"} {
		if _, err := r.draft(Fact{Kind: KindExpense, Amount: s, AmountRole: "paid"}, nowForTest()); err == nil {
			t.Errorf("accepted invalid amount %q", s)
		}
	}
}

func TestTransactionTimes(t *testing.T) {
	for _, tc := range []struct{ date, clock, want string }{{"", "17:37", "2026-10-30 17:37"}, {"yesterday", "20:02", "2026-10-29 20:02"}, {"10-29", "", "2026-10-29 12:00"}, {"", "", "2026-10-30 21:32"}, {"2026-10-30", "19:48:30", "2026-10-30 19:48"}} {
		got, _, err := transactionTime(tc.date, tc.clock, nowForTest(), nowForTest().Location())
		if err != nil || got.Format("2006-01-02 15:04") != tc.want {
			t.Errorf("%s %s => %s %v", tc.date, tc.clock, got, err)
		}
	}
	if _, _, err := transactionTime("2026-10-30", "99:99", nowForTest(), nowForTest().Location()); err == nil {
		t.Fatal("invalid time accepted")
	}
}

func TestCorrectionPreservesUnmentionedFields(t *testing.T) {
	r := &Recognizer{Config: configForTest(), Router: chatFunc(func(_ context.Context, _ openrouter.ChatRequest, out any) (*openrouter.ChatResponse, error) {
		return &openrouter.ChatResponse{}, decodeInto(map[string]any{"amount": "21.00"}, out)
	})}
	d := &Draft{Status: "ok", Kind: KindExpense, Money: "20.00", Currency: "CNY", Time: nowForTest(), Merchant: "超市", AssetID: "credit", CategoryID: "other", Payment: "花呗", Uncertain: []string{"asset", "money"}}
	r.Decider = decisionFunc(func(context.Context, openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error) {
		t.Fatal("amount-only edit reclassified bill")
		return nil, nil
	})
	next, _, err := r.Correct(context.Background(), d, catalogForTest, "金额改成21", nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if next.Money != "21.00" || next.AssetID != "credit" || next.CategoryID != "other" || next.Merchant != "超市" || !next.Time.Equal(d.Time) || !contains(next.Uncertain, "asset") || contains(next.Uncertain, "money") {
		t.Fatalf("unexpected edit: %+v", next)
	}
	if d.Money != "20.00" || !contains(d.Uncertain, "money") {
		t.Fatal("original draft mutated")
	}
}
