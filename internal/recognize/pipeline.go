// Package recognize turns a screenshot into a bill draft: OCR, candidate
// extraction, one decision-model request, and a vision LLM fallback.
package recognize

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/imgutil"
	"ledger-snap/internal/ocr"
	"ledger-snap/internal/openrouter"
	"ledger-snap/internal/qianji"
)

type Kind string

const (
	KindExpense  Kind = "expense"
	KindIncome   Kind = "income"
	KindTransfer Kind = "transfer"
	KindCredit   Kind = "credit"
	KindRefund   Kind = "refund"
	KindNone     Kind = "none"
)

var kindLabels = map[Kind]string{
	KindExpense: "支出", KindIncome: "收入", KindTransfer: "转账", KindCredit: "信用卡还款", KindRefund: "退款",
}

func (k Kind) Label() string { return kindLabels[k] }

// IsTransfer reports a move between the user's own accounts: a transfer or a
// credit repayment. These have source and target accounts but no category.
func (k Kind) IsTransfer() bool { return k == KindTransfer || k == KindCredit }

// Draft is the recognized bill before it is written.
type Draft struct {
	Status     string    `json:"status"` // ok | rejected
	Reason     string    `json:"reason,omitempty"`
	Kind       Kind      `json:"kind"`
	Money      string    `json:"money,omitempty"`
	Time       time.Time `json:"time"`
	TimeSource string    `json:"time_source,omitempty"` // screenshot | upload
	CategoryID string    `json:"category_id,omitempty"`
	AssetID    string    `json:"asset_id,omitempty"`  // paying account; source for transfers
	TargetID   string    `json:"target_id,omitempty"` // receiving account for transfers and repayments
	Merchant   string    `json:"merchant,omitempty"`
	Uncertain  []string  `json:"uncertain,omitempty"`
	Source     string    `json:"source"` // decision | fallback
	// Currency is the code of Money; "" means the account's currency. For a
	// transfer, Money is the amount sent.
	Currency string `json:"currency,omitempty"`
	// Converted is the same bill in another currency, when the screenshot
	// shows one: what a currency exchange bought, or what a foreign purchase
	// cost in the card's currency. "" currency means the other account's.
	Converted         string `json:"converted,omitempty"`
	ConvertedCurrency string `json:"converted_currency,omitempty"`
}

func (d *Draft) mark(field string) {
	if !slices.Contains(d.Uncertain, field) {
		d.Uncertain = append(d.Uncertain, field)
	}
}

// Memory holds learned rules: payment text to asset, merchant to category.
type Memory struct {
	AssetAliases  map[string]string
	MerchantRules map[string]string
}

// Trace records everything needed to replay or debug one recognition.
type Trace struct {
	OCRProvider   string                        `json:"ocr_provider"`
	OCRLines      []ocr.Line                    `json:"ocr_lines"`
	OCRMs         int64                         `json:"ocr_ms"`
	Amounts       []AmountCandidate             `json:"amounts"`
	Times         []TimeCandidate               `json:"times"`
	DecisionModel string                        `json:"decision_model"`
	DecisionMs    int64                         `json:"decision_ms"`
	Decision      *openrouter.DecisionsResponse `json:"decision,omitempty"`
	DecisionError string                        `json:"decision_error,omitempty"`
	FallbackModel string                        `json:"fallback_model,omitempty"`
	FallbackMs    int64                         `json:"fallback_ms,omitempty"`
	Fallback      *FallbackResult               `json:"fallback,omitempty"`
	FallbackError string                        `json:"fallback_error,omitempty"`
	FallbackWhy   string                        `json:"fallback_why,omitempty"`
	MemoryHits    []string                      `json:"memory_hits,omitempty"`
	// CatalogRefresh notes a catalog refresh after the first try matched no
	// account or category, and what came of it.
	CatalogRefresh string `json:"catalog_refresh,omitempty"`
	// Truncated lists option lists cut to fit the per-question limits.
	Truncated []string `json:"truncated,omitempty"`
	TotalMs   int64    `json:"total_ms"`
	Cost      float64  `json:"cost"`
}

// Decider answers typed decision questions: OpenRouter's Decisions API or
// TypeSafe's own System One API.
type Decider interface {
	Decide(ctx context.Context, req openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error)
}

type Recognizer struct {
	Config  *config.Config
	OCR     ocr.Engine
	Decider Decider
	// Router serves the chat models: vision fallback and correction parsing.
	Router *openrouter.Client
}

type Input struct {
	Image      []byte
	UploadTime time.Time
	Catalog    qianji.Catalog
	Memory     Memory
	// Lines reuses an earlier OCR result of the same image; nil runs OCR.
	Lines []ocr.Line
}

func (r *Recognizer) Recognize(ctx context.Context, in Input) (*Draft, *Trace, error) {
	start := time.Now()
	cfg := r.Config
	loc := cfg.Location()
	trace := &Trace{OCRProvider: r.OCR.Name(), DecisionModel: cfg.Decision.Provider + ":" + cfg.Decision.Model}
	defer func() { trace.TotalMs = time.Since(start).Milliseconds() }()

	lines := in.Lines
	var err error
	if lines == nil {
		var took time.Duration
		lines, took, err = ocr.Timed(ctx, r.OCR, in.Image)
		trace.OCRMs = took.Milliseconds()
		if err != nil {
			return nil, trace, err
		}
	}
	trace.OCRLines = lines
	_, imageHeight, _ := imgutil.Size(in.Image)
	var rows []Row
	for _, row := range Rows(lines) {
		if !statusBar(row, imageHeight) {
			rows = append(rows, row)
		}
	}
	trace.Amounts = capList(trace, "金额候选", Amounts(rows, 0), cfg.Decision.MaxCandidates)
	trace.Times = capList(trace, "时间候选", Times(rows, in.UploadTime, loc, 0), cfg.Decision.MaxCandidates)
	if len(rows) == 0 {
		return reject("截图里没有识别到文字"), trace, nil
	}

	var small []byte
	if cfg.Decision.SendImage || cfg.Fallback.Enabled {
		small, err = imgutil.Fit(in.Image, cfg.Decision.ImageMaxSide, cfg.Decision.ImageMinSide, cfg.Decision.JPEGQuality)
		if err != nil {
			return nil, trace, err
		}
	}
	q := r.buildQuestions(rows, trace, in)
	dStart := time.Now()
	resp, err := r.Decider.Decide(ctx, openrouter.DecisionsRequest{Model: cfg.Decision.Model, State: r.state(rows, in, small), Questions: q.questions})
	trace.DecisionMs = time.Since(dStart).Milliseconds()
	var draft *Draft
	why := ""
	if err != nil {
		trace.DecisionError = err.Error()
		why = "决策模型请求失败"
	} else {
		trace.Decision = resp
		trace.Cost += resp.Usage.Cost
		draft, why = r.interpret(resp.Answers, q, trace, in)
	}
	if why != "" && cfg.Fallback.Enabled {
		trace.FallbackWhy = why
		fb, err := r.fallback(ctx, rows, in, small, trace)
		if err != nil {
			trace.FallbackError = err.Error()
		} else {
			draft = fb
		}
	}
	if draft == nil {
		if why == "" {
			why = "无法识别"
		}
		return reject("没能认出这笔交易：" + why), trace, nil
	}
	if draft.Kind.IsTransfer() {
		// The "merchant" row of a transfer is usually an account line; the app
		// already shows transfers as "A->B".
		draft.Merchant = ""
	}
	r.applyMemory(draft, rows, in, trace)
	r.finishTime(draft, in)
	finishCurrency(draft, in.Catalog)
	return draft, trace, nil
}

func reject(reason string) *Draft { return &Draft{Status: "rejected", Reason: reason, Kind: KindNone} }

const intro = "这是用户在手机上截取的一张屏幕截图，可能是支付成功页、待支付或确认付款页、账单详情、收款或转账页面，也可能与交易无关。"

func (r *Recognizer) state(rows []Row, in Input, image []byte) []any {
	state := []any{intro}
	if r.Config.Decision.SendImage && image != nil {
		state = append(state, openrouter.ImagePart(image))
	}
	state = append(state, "截图中 OCR 识别出的文字，从上到下：\n"+rowsText(rows))
	state = append(state, "用户上传截图的时间（北京时间）："+in.UploadTime.In(r.Config.Location()).Format("2006-01-02 15:04"))
	return state
}

func rowsText(rows []Row) string {
	var b strings.Builder
	for i, row := range rows {
		fmt.Fprintf(&b, "%d. %s\n", i+1, row.Text)
	}
	return b.String()
}

type questionSet struct {
	questions  map[string]openrouter.Question
	categories map[string]string // option key -> category id
	assets     map[string]string // option key -> asset id
	merchants  map[string]string // option key -> row text
}

func (r *Recognizer) buildQuestions(rows []Row, trace *Trace, in Input) questionSet {
	qs := questionSet{questions: map[string]openrouter.Question{}, categories: map[string]string{}, assets: map[string]string{}, merchants: map[string]string{}}
	// Payments not finished yet still count: users often trigger the shortcut
	// on the confirm sheet, before the success page.
	qs.questions["transaction"] = openrouter.NoulQuestion(
		"截图显示的是一笔要记账的具体交易：已经完成，或者正在付款、即将付款。",
		"页面显示了一笔具体交易，包括支付成功、交易成功、已收款、转账成功、提现成功、还款成功、退款成功、扣款成功，也包括待支付、确认付款、Apple Pay 等付款确认弹窗、收银台、处理中、待对方收款。",
		"页面与交易无关，或交易已经失败、取消、关闭，或者是列出多笔交易的账单列表、只显示余额的钱包页、商品页、行情或新闻。")
	qs.questions["kind"] = openrouter.ChoiceQuestion("对用户来说，这笔交易属于哪一类？", []openrouter.Option{
		{Key: string(KindExpense), Description: "支出：用户把钱付给商户或其他人，包括购物、消费、给别人转账、发红包、AA 付款"},
		{Key: string(KindIncome), Description: "收入：用户从别人那里收到钱，包括收到别人的转账、收红包、工资、收款"},
		{Key: string(KindTransfer), Description: "自己账户之间转账：钱从用户自己的一个账户转到自己的另一个账户，例如零钱提现到自己的银行卡、银行卡充值到零钱、转入或转出余额宝。转给别人不算"},
		{Key: string(KindCredit), Description: "还款：用户用自己的账户还自己的信用卡、花呗、白条等信用账户"},
		{Key: string(KindRefund), Description: "退款：之前的一笔支出被退回"},
		{Key: string(KindNone), Description: "不是交易记录"},
	})
	amounts := []openrouter.Option{{Key: "none", Description: "截图里没有这笔交易的金额"}}
	foreign := false
	for _, a := range trace.Amounts {
		sign, currency := "", ""
		if a.Negative {
			sign = "-"
		} else if a.Positive {
			sign = "+"
		}
		if a.Currency != "" {
			currency = " " + a.Currency
		}
		foreign = foreign || (a.Currency != "" && a.Currency != "CNY")
		amounts = append(amounts, openrouter.Option{Key: a.Key, Description: fmt.Sprintf("%s%s%s（所在行：%s）", sign, a.Value, currency, a.Context)})
	}
	qs.questions["amount"] = openrouter.ChoiceQuestion("这笔交易实际付款或到账的金额是哪一个？还没付款时选要付的金额；有优惠时选优惠后的实付金额，不要选原价、优惠金额、余额或订单号。换汇或跨币种转账时选卖出、转出的金额；外币消费同时显示原币和折算金额时选原币金额。", amounts)
	if foreign {
		// Only screenshots that show a foreign currency can carry a second
		// amount in another currency.
		qs.questions["converted"] = openrouter.ChoiceQuestion("截图里有没有同一笔交易换成另一种币种的金额？换汇或跨币种转账时是买入、转入的金额；外币消费时是账户实际扣款、入账或折合人民币的金额。",
			append([]openrouter.Option{{Key: "none", Description: "没有另一种币种的金额"}}, amounts[1:]...))
	}
	times := []openrouter.Option{{Key: "none", Description: "截图里没有这笔交易的发生时间（顶部状态栏的时钟不算）"}}
	for _, t := range trace.Times {
		times = append(times, openrouter.Option{Key: t.Key, Description: fmt.Sprintf("%s（所在行：%s）", t.Text, t.Context)})
	}
	qs.questions["time"] = openrouter.ChoiceQuestion("这笔交易发生的时间是哪一个？", times)
	for _, kind := range []int{qianji.TypeExpense, qianji.TypeIncome} {
		name, what := "category_expense", "支出"
		if kind == qianji.TypeIncome {
			name, what = "category_income", "收入"
		}
		var opts []openrouter.Option
		for _, c := range capList(trace, what+"分类", in.Catalog.Leaves(kind), MaxChoiceOptions) {
			key := "c" + c.ID
			qs.categories[key] = c.ID
			opts = append(opts, openrouter.Option{Key: key, Description: c.Path})
		}
		if len(opts) > 0 {
			qs.questions[name] = openrouter.ChoiceQuestion("如果这是一笔"+what+"，记到哪个分类最合适？", opts)
		}
	}
	if len(in.Catalog.Assets) > 0 {
		var opts []openrouter.Option
		for _, a := range capList(trace, "账户", in.Catalog.Assets, MaxChoiceOptions) {
			key := "s" + a.ID
			qs.assets[key] = a.ID
			opts = append(opts, openrouter.Option{Key: key, Description: AssetLabel(a)})
		}
		qs.questions["asset"] = openrouter.ChoiceQuestion("如果是支出或收入：付款或收款用的是用户的哪个账户？看支付方式、付款方式、收款账户、银行名称和卡号尾号，尾号也可能写在账户备注里。",
			append([]openrouter.Option{{Key: "none", Description: "截图里看不出账户，或者下面的账户都对不上"}}, opts...))
		if len(in.Catalog.Assets) > 1 {
			qs.questions["source_asset"] = openrouter.ChoiceQuestion("如果是用户自己账户之间转账或还款：钱从哪个账户转出？零钱提现、余额转出时，转出的是零钱或余额本身；还款时是付款的账户。",
				append([]openrouter.Option{{Key: "none", Description: "不是自己账户之间的转账或还款，或者看不出转出账户"}}, opts...))
			qs.questions["target_asset"] = openrouter.ChoiceQuestion("如果是用户自己账户之间转账或还款：钱转入哪个账户？提现时是到账的银行卡，还款时是被还的信用卡或花呗。",
				append([]openrouter.Option{{Key: "none", Description: "不是自己账户之间的转账或还款，或者看不出转入账户"}}, opts...))
		}
	}
	var merchants []openrouter.Option
	for i, row := range capList(trace, "文字行", rows, r.Config.Decision.MaxCandidates) {
		key := fmt.Sprintf("l%d", i)
		qs.merchants[key] = row.Text
		merchants = append(merchants, openrouter.Option{Key: key, Description: row.Text})
	}
	qs.questions["merchant"] = openrouter.ChoiceQuestion("哪一行写的是交易对方，例如商户名称、收款方或付款人？", merchants)
	return qs
}

// MaxChoiceOptions is Jev's limit of 255 options per choice question, minus
// the "none" option most questions add.
const MaxChoiceOptions = 254

// capList cuts a list to n items and records the cut in the trace.
func capList[T any](trace *Trace, what string, items []T, n int) []T {
	if n > 0 && len(items) > n {
		trace.Truncated = append(trace.Truncated, fmt.Sprintf("%s共 %d 个，只保留了前 %d 个", what, len(items), n))
		return items[:n]
	}
	return items
}

func limit[T any](items []T, n int) []T {
	if n > 0 && len(items) > n {
		return items[:n]
	}
	return items
}

// interpret maps answers to a draft. A non-empty reason means the answers
// were not trustworthy enough and the fallback should run.
func (r *Recognizer) interpret(ans map[string]openrouter.Answer, qs questionSet, trace *Trace, in Input) (*Draft, string) {
	th := r.Config.Decision.Thresholds
	transaction := ans["transaction"].Noul
	kind := ans["kind"]
	switch {
	case transaction < 0.25 && kind.Prob() >= th.Type:
		return reject("截图里没有要记账的交易"), ""
	case transaction < th.Transaction:
		return nil, fmt.Sprintf("不确定是不是要记账的交易（%.2f）", transaction)
	case kind.Prob() < th.Type:
		return nil, fmt.Sprintf("交易类型不确定（%s %.2f）", kind.Choice, kind.Prob())
	}
	d := &Draft{Status: "ok", Kind: Kind(kind.Choice), Source: "decision"}
	if d.Kind == KindNone {
		return reject("截图不是交易记录"), ""
	}
	money, p := pickAmount(trace.Amounts, ans["amount"])
	if money == "" || p < th.Amount {
		return nil, fmt.Sprintf("金额不确定（%s %.2f）", ans["amount"].Choice, p)
	}
	d.Money, d.Currency = money, currencyOf(trace.Amounts, money)
	if c, ok := ans["converted"]; ok {
		if v, p := pickAmount(trace.Amounts, c); v != "" && p >= th.Amount {
			if cur := currencyOf(trace.Amounts, v); v != money || cur != d.Currency {
				d.Converted, d.ConvertedCurrency = v, cur
			}
		}
	}
	if t := ans["time"]; t.Choice != "none" {
		for _, c := range trace.Times {
			if c.Key == t.Choice {
				d.Time, d.TimeSource = c.Time, "screenshot"
			}
		}
		if t.Prob() < th.Time {
			d.mark("time")
		}
	}
	if d.Kind == KindExpense || d.Kind == KindIncome {
		catQ := "category_expense"
		if d.Kind == KindIncome {
			catQ = "category_income"
		}
		if c, ok := ans[catQ]; ok {
			d.CategoryID = qs.categories[c.Choice]
			if c.Prob() < th.Category {
				d.mark("category")
			}
		}
	}
	assetQ := "asset"
	if d.Kind.IsTransfer() {
		assetQ = "source_asset"
	}
	if a, ok := ans[assetQ]; ok && a.Choice != "none" {
		d.AssetID = qs.assets[a.Choice]
		if a.Prob() < th.Asset {
			d.mark("asset")
		}
	}
	if d.Kind.IsTransfer() {
		if t, ok := ans["target_asset"]; ok && t.Choice != "none" && qs.assets[t.Choice] != d.AssetID {
			d.TargetID = qs.assets[t.Choice]
			if t.Prob() < th.Asset {
				d.mark("target_asset")
			}
		}
	}
	if m, ok := ans["merchant"]; ok {
		d.Merchant = merchantName(qs.merchants[m.Choice])
	}
	return d, ""
}

// pickAmount returns the most likely amount value. The same amount often
// shows on several rows (the big number, the total, the paid line), and the
// model splits its probability among them, so rows with one value add up.
func pickAmount(cands []AmountCandidate, ans openrouter.Answer) (string, float64) {
	sums := map[string]float64{}
	for _, c := range cands {
		sums[c.Value] += ans.Probabilities[c.Key]
	}
	money, p := "", 0.0
	for _, c := range cands {
		if sums[c.Value] > p {
			money, p = c.Value, sums[c.Value]
		}
	}
	return money, p
}

// currencyOf returns the currency shown with any row that has this amount.
func currencyOf(cands []AmountCandidate, value string) string {
	for _, c := range cands {
		if c.Value == value && c.Currency != "" {
			return c.Currency
		}
	}
	return ""
}

// finishCurrency resolves a bare "$" against the accounts' currencies. A
// detected currency is kept even when it matches the account, so switching
// to another account later does not change what was paid.
func finishCurrency(d *Draft, c qianji.Catalog) {
	if d.Status != "ok" {
		return
	}
	var accounts []string
	for _, id := range []string{d.AssetID, d.TargetID} {
		if cur, ok := c.AssetCurrency(id); ok {
			accounts = append(accounts, cur)
		}
	}
	d.Currency = resolveDollar(d.Currency, accounts...)
	d.ConvertedCurrency = resolveDollar(d.ConvertedCurrency, accounts...)
	if d.Converted == "" {
		d.ConvertedCurrency = ""
	}
}

// merchantName strips a leading field label such as "交易商户 ".
func merchantName(row string) string {
	for _, label := range []string{"交易商户", "收款方", "商户名称", "商户", "对方账户", "转账说明", "商品说明", "交易对方", "付给"} {
		if rest, ok := strings.CutPrefix(row, label); ok {
			return strings.TrimSpace(strings.TrimLeft(rest, ":："))
		}
	}
	return strings.TrimSpace(row)
}

// applyMemory overrides model choices with what the user taught us before.
func (r *Recognizer) applyMemory(d *Draft, rows []Row, in Input, trace *Trace) {
	if d.Status != "ok" || (d.Kind != KindExpense && d.Kind != KindIncome) {
		return
	}
	if id, ok := assetFromMemory(rows, in.Memory); ok {
		if _, exists := in.Catalog.Asset(id); exists {
			d.AssetID = id
			trace.MemoryHits = append(trace.MemoryHits, "asset")
			d.Uncertain = remove(d.Uncertain, "asset")
		}
	}
	if id, ok := in.Memory.MerchantRules[d.Merchant]; ok && d.Merchant != "" {
		if c, exists := in.Catalog.Category(id); exists && c.Type == kindType(d.Kind) {
			d.CategoryID = id
			trace.MemoryHits = append(trace.MemoryHits, "category")
			d.Uncertain = remove(d.Uncertain, "category")
		}
	}
}

// AssetLabel names an asset with its remark, e.g. "工行卡（储蓄卡 尾号5678）".
func AssetLabel(a qianji.Asset) string {
	if a.Remark != "" {
		return a.Name + "（" + a.Remark + "）"
	}
	return a.Name
}

func assetFromMemory(rows []Row, m Memory) (string, bool) {
	for _, row := range rows {
		for pattern, id := range m.AssetAliases {
			if pattern != "" && strings.Contains(row.Text, pattern) {
				return id, true
			}
		}
	}
	return "", false
}

func remove(list []string, v string) []string {
	out := list[:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func kindType(k Kind) int {
	if k == KindIncome {
		return qianji.TypeIncome
	}
	return qianji.TypeExpense
}

// finishTime falls back to the upload time and flags stale screenshots.
func (r *Recognizer) finishTime(d *Draft, in Input) {
	if d.Status != "ok" {
		return
	}
	if d.Time.IsZero() {
		d.Time, d.TimeSource = in.UploadTime, "upload"
		return
	}
	if in.UploadTime.Sub(d.Time) > r.Config.Recognize.StaleScreenshot.Duration {
		d.mark("time")
	}
}
