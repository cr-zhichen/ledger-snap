package recognize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/ocr"
	"ledger-snap/internal/openrouter"
)

// shifted is a payment page whose values sit lower than their labels, so
// boxes alone put each on a row of its own.
var shifted = []ocr.Line{
	{Text: "支付成功", X: 500, Y: 100, W: 200, H: 50},
	{Text: "¥25.00", X: 400, Y: 200, W: 400, H: 100},
	{Text: "支付方式", X: 90, Y: 400, W: 180, H: 50},
	{Text: "招商银行信用卡（1234）", X: 600, Y: 440, W: 440, H: 50},
	{Text: "Merchant", X: 90, Y: 500, W: 180, H: 50},
	{Text: "LUCKIN", X: 600, Y: 470, W: 180, H: 50},
	{Text: "COFFEE", X: 600, Y: 530, W: 180, H: 50},
}

func TestItemRows(t *testing.T) {
	rows := itemRows(shifted, []RowItem{
		{Value: []int{0}},
		// A value above its label still follows it; repeated and unknown
		// numbers are dropped.
		{Label: []int{4}, Value: []int{6, 5, 5, 99}},
		{Label: []int{2}, Value: []int{3}},
		// Left out: line 1 gets a row of its own.
	})
	var got []string
	for _, row := range rows {
		got = append(got, row.Text)
	}
	want := []string{"支付成功", "¥25.00", "支付方式 招商银行信用卡(1234)", "Merchant LUCKIN COFFEE"}
	if !slices.Equal(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	if merchantOf(rows[3]) != "LUCKIN COFFEE" || merchantOf(rows[0]) != "支付成功" {
		t.Errorf("merchants = %q, %q", merchantOf(rows[3]), merchantOf(rows[0]))
	}
	// A label with nothing to label is plain text.
	if rows := itemRows(shifted[:1], []RowItem{{Label: []int{0}}}); rows[0].Text != "支付成功" || rows[0].Label != "" {
		t.Errorf("label-only row = %+v", rows[0])
	}
}

// fakeRouter serves OpenRouter chat completions with reply, after delay.
func fakeRouter(t *testing.T, delay time.Duration, status int, reply string) (*openrouter.Client, *[]map[string]any) {
	t.Helper()
	var reqs []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		reqs = append(reqs, req)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
			"usage":   map[string]any{"cost": 0.001},
		})
	}))
	t.Cleanup(srv.Close)
	return openrouter.New(srv.URL, "key", "", 5*time.Second), &reqs
}

func rowsRecognizer(router *openrouter.Client) *Recognizer {
	cfg := &config.Config{}
	cfg.Rows = config.Rows{Enabled: true, Model: "qwen/qwen3.8-27b", Provider: "cerebras", SchemaInPrompt: true, ReasoningEffort: "none", Timeout: config.Duration{Duration: time.Second}}
	return &Recognizer{Config: cfg, Router: router}
}

func TestRowsModel(t *testing.T) {
	router, reqs := fakeRouter(t, 0, 200, "```json\n"+`{"items":[{"label":[],"value":[0]},{"label":[],"value":[1]},{"label":[2],"value":[3]},{"label":[4],"value":[5,6]}]}`+"\n```")
	r := rowsRecognizer(router)
	trace := &Trace{}
	rows := r.rows(context.Background(), shifted, nil, 1179, 2556, trace)
	if len(rows) != 4 || trace.RowsError != "" || len(trace.RowItems) != 4 || trace.Cost != 0.001 {
		t.Fatalf("rows = %+v, trace = %+v", rows, trace)
	}
	if p := trace.PaymentText(); p != "招商银行信用卡(1234)" {
		t.Errorf("payment = %q", p)
	}
	req := (*reqs)[0]
	prompt := req["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	provider, _ := json.Marshal(req["provider"])
	// The provider cannot be held to a schema, so the prompt carries it.
	if req["response_format"] != nil || string(provider) != `{"allow_fallbacks":false,"order":["cerebras"],"require_parameters":false}` ||
		!strings.Contains(prompt, "JSON Schema") || !strings.Contains(prompt, "3 [x=600 y=440 w=440 h=50] 招商银行信用卡(1234)") {
		t.Errorf("request = %v", req)
	}
	if effort := req["reasoning"].(map[string]any)["effort"]; effort != "none" {
		t.Errorf("reasoning effort = %v", effort)
	}

	// The same items are reused without asking again.
	again := &Trace{}
	r.rows(context.Background(), shifted, trace.RowItems, 1179, 2556, again)
	if len(*reqs) != 1 || !slices.Equal(again.Rows, trace.Rows) {
		t.Errorf("reuse asked %d times, rows %q", len(*reqs), again.Rows)
	}
}

func TestRowsModelFailure(t *testing.T) {
	byPosition := len(Rows(shifted))
	for name, router := range map[string]*openrouter.Client{
		"error":   first(fakeRouter(t, 0, 500, "")),
		"timeout": first(fakeRouter(t, 2*time.Second, 200, `{"items":[]}`)),
		"no json": first(fakeRouter(t, 0, 200, "好的")),
	} {
		trace := &Trace{}
		rows := rowsRecognizer(router).rows(context.Background(), shifted, nil, 1179, 2556, trace)
		if len(rows) != byPosition || trace.RowsError == "" || trace.RowItems != nil {
			t.Errorf("%s: %d rows, trace = %+v", name, len(rows), trace)
		}
	}
}

func first[A, B any](a A, _ B) A { return a }

// noDecider fails the test when the decision model is asked.
type noDecider struct{ t *testing.T }

func (d noDecider) Decide(context.Context, openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error) {
	d.t.Error("decision model asked")
	return nil, context.Canceled
}

// failingDecider fails like an unreachable decision model and keeps the
// request it got.
type failingDecider struct{ req openrouter.DecisionsRequest }

func (d *failingDecider) Decide(_ context.Context, req openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error) {
	d.req = req
	return nil, errors.New("HTTP 502")
}

// TestFallbackChain walks every step down: the rows model fails, so the rows
// come from box positions; the decision model fails on those rows, so the
// fallback model reads the screenshot.
func TestFallbackChain(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openrouter.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req.Model)
		if req.Model == "qwen/qwen3.8-27b" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"is_transaction":true,"is_failed":false,"kind":"expense","amount":"25.00","currency":"CNY","converted_amount":"","converted_currency":"","time":"2026-10-22 12:31:05","category_id":"101","asset_id":"201","target_asset_id":"","discount":"","fee":"","merchant":"美团","reason":""}`}}},
		})
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.Recognize.Timezone = "Asia/Shanghai"
	cfg.Decision.MaxCandidates = 40
	cfg.Decision.ImageMaxSide = 1568
	cfg.Decision.JPEGQuality = 80
	cfg.Rows = config.Rows{Enabled: true, Model: "qwen/qwen3.8-27b", Provider: "cerebras", SchemaInPrompt: true, Timeout: config.Duration{Duration: time.Second}}
	cfg.Fallback = config.Fallback{Enabled: true, Model: "anthropic/claude-haiku-5.5"}
	decider := &failingDecider{}
	r := &Recognizer{Config: cfg, OCR: fakeOCR{}, Decider: decider, Router: openrouter.New(srv.URL, "key", "", 5*time.Second)}
	var img bytes.Buffer
	_ = jpeg.Encode(&img, image.NewGray(image.Rect(0, 0, 300, 600)), nil)
	lines := []ocr.Line{
		{Text: "支付成功", X: 500, Y: 100, W: 200, H: 50},
		{Text: "¥25.00", X: 400, Y: 200, W: 400, H: 100},
		{Text: "支付方式", X: 90, Y: 400, W: 180, H: 50},
		{Text: "招商银行信用卡(1234)", X: 600, Y: 440, W: 440, H: 50},
	}
	draft, trace, err := r.Recognize(context.Background(), Input{
		Image: img.Bytes(), UploadTime: time.Date(2026, 10, 22, 12, 35, 0, 0, beijing), Catalog: historyCatalog, Lines: lines,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(asked, []string{"qwen/qwen3.8-27b", "anthropic/claude-haiku-5.5"}) {
		t.Errorf("asked %v", asked)
	}
	// The rows model failed: rows by position, the label apart from its value.
	if trace.RowsError == "" || trace.RowItems != nil || !slices.Equal(trace.Rows, []string{"支付成功", "¥25.00", "支付方式", "招商银行信用卡(1234)"}) {
		t.Errorf("rows = %q, error = %q", trace.Rows, trace.RowsError)
	}
	var state []string
	for _, s := range decider.req.State {
		if text, ok := s.(string); ok {
			state = append(state, text)
		}
	}
	if !strings.Contains(strings.Join(state, "\n"), "3. 支付方式\n4. 招商银行信用卡(1234)") {
		t.Errorf("decision model saw %q", state)
	}
	if trace.DecisionError == "" || trace.FallbackWhy != "决策模型请求失败" || trace.FallbackModel != "anthropic/claude-haiku-5.5" {
		t.Errorf("trace = %+v", trace)
	}
	if draft.Status != "ok" || draft.Source != "fallback" || draft.Money != "25.00" || draft.AssetID != "201" {
		t.Errorf("draft = %+v", draft)
	}
}

func TestFallbackPrimary(t *testing.T) {
	router, reqs := fakeRouter(t, 0, 200, `{"is_transaction":true,"is_failed":false,"kind":"expense","amount":"25.00","currency":"CNY","converted_amount":"","converted_currency":"","time":"2026-10-22 12:31:05","category_id":"101","asset_id":"201","target_asset_id":"","discount":"","fee":"","merchant":"美团","reason":""}`)
	cfg := &config.Config{}
	cfg.Recognize.Timezone = "Asia/Shanghai"
	cfg.Decision.MaxCandidates = 40
	cfg.Decision.ImageMaxSide = 1568
	cfg.Decision.JPEGQuality = 80
	cfg.Fallback = config.Fallback{Enabled: true, Primary: true, Model: "qwen/qwen3.8-27b", Provider: "cerebras", SchemaInPrompt: true}
	r := &Recognizer{Config: cfg, OCR: fakeOCR{}, Decider: noDecider{t}, Router: router}
	var img bytes.Buffer
	_ = jpeg.Encode(&img, image.NewGray(image.Rect(0, 0, 300, 600)), nil)
	draft, trace, err := r.Recognize(context.Background(), Input{
		Image: img.Bytes(), UploadTime: time.Date(2026, 10, 22, 12, 35, 0, 0, beijing), Catalog: historyCatalog, Lines: shifted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Source != "fallback" || draft.Money != "25.00" || draft.CategoryID != "101" || draft.Merchant != "美团" || trace.Decision != nil || trace.FallbackWhy == "" {
		t.Errorf("draft = %+v, trace = %+v", draft, trace)
	}
	// The screenshot goes along with the text.
	if parts := (*reqs)[0]["messages"].([]any)[0].(map[string]any)["content"].([]any); len(parts) != 2 {
		t.Errorf("content parts = %d", len(parts))
	}
}
