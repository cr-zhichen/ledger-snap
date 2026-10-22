package recognize

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledger-snap/internal/openrouter"
	"ledger-snap/internal/qianji"
)

const keep = "keep"

type CorrectionTrace struct {
	DecisionModel string                        `json:"decision_model"`
	Decision      *openrouter.DecisionsResponse `json:"decision,omitempty"`
	LLMModel      string                        `json:"llm_model,omitempty"`
	LLM           *correctionFields             `json:"llm,omitempty"`
	Ms            int64                         `json:"ms"`
	Cost          float64                       `json:"cost"`
}

type correctionFields struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Discount string `json:"discount"`
	Fee      string `json:"fee"`
	Time     string `json:"time"`
	Merchant string `json:"merchant"`
}

// moneyText shows a draft's amount with its currency and the converted
// amount, e.g. "3940.45 HKD（另一币种 500.00 USD）".
func moneyText(d *Draft) string {
	s := d.Money
	if d.Currency != "" {
		s += " " + d.Currency
	}
	if d.Converted != "" {
		s += "（另一币种 " + strings.TrimSpace(d.Converted+" "+d.ConvertedCurrency) + "）"
	}
	if d.Discount != "" {
		s += "（优惠 " + d.Discount + "）"
	}
	if d.Fee != "" {
		s += "（含手续费 " + d.Fee + "）"
	}
	return s
}

// Describe renders a draft as plain text for the models.
func Describe(d *Draft, c qianji.Catalog, loc *time.Location) string {
	asset := func(id, empty string) string {
		if a, ok := c.Asset(id); ok {
			return AssetLabel(a)
		}
		return empty
	}
	when := d.Time.In(loc).Format("2006-01-02 15:04")
	if d.Kind.IsTransfer() {
		return fmt.Sprintf("类型：%s；金额：%s；转出账户：%s；转入账户：%s；时间：%s；备注：%s",
			d.Kind.Label(), moneyText(d), asset(d.AssetID, "未选择"), asset(d.TargetID, "未选择"), when, d.Merchant)
	}
	category := "未分类"
	if cat, ok := c.Category(d.CategoryID); ok {
		category = cat.Path
	}
	return fmt.Sprintf("类型：%s；金额：%s；分类：%s；账户：%s；时间：%s；备注：%s",
		d.Kind.Label(), moneyText(d), category, asset(d.AssetID, "不关联账户"), when, d.Merchant)
}

// Correct applies one free-text correction to a draft and returns the new draft.
func (r *Recognizer) Correct(ctx context.Context, d *Draft, catalog qianji.Catalog, text string, now time.Time) (*Draft, *CorrectionTrace, error) {
	start := time.Now()
	loc := r.Config.Location()
	tr := &CorrectionTrace{DecisionModel: r.Config.Decision.Model}
	defer func() { tr.Ms = time.Since(start).Milliseconds() }()
	state := []any{
		"用户刚用截图记了一笔账：" + Describe(d, catalog, loc),
		"用户的修改意见：" + text,
		"现在时间（北京时间）：" + now.In(loc).Format("2006-01-02 15:04"),
	}
	qs := map[string]openrouter.Question{
		"kind": openrouter.ChoiceQuestion("修改后这笔账是什么类型？", []openrouter.Option{
			{Key: keep, Description: "用户没有提到要改类型"},
			{Key: string(KindExpense), Description: "支出（包括转给别人、发红包）"},
			{Key: string(KindIncome), Description: "收入（包括收到别人的转账、红包）"},
			{Key: string(KindTransfer), Description: "自己账户之间转账"},
			{Key: string(KindCredit), Description: "还自己的信用卡、花呗"},
		}),
		"amount": openrouter.NoulQuestion("用户要求修改金额（优惠和手续费不算）。", "修改意见里给出了新的金额。", "修改意见没有提到金额。"),
		"adjust": openrouter.NoulQuestion("用户要求修改优惠或手续费。", "修改意见里给出了新的优惠或手续费，或者说没有优惠、没有手续费。", "修改意见没有提到优惠或手续费。"),
		"time":   openrouter.NoulQuestion("用户要求修改时间或日期。", "修改意见里给出了新的时间或日期，例如昨天、上午十点。", "修改意见没有提到时间。"),
		"remark": openrouter.NoulQuestion("用户要求修改备注或商户名称。", "修改意见里给出了新的备注或商户。", "修改意见没有提到备注或商户。"),
	}
	categoryKeys := map[string]string{}
	for _, kind := range []int{qianji.TypeExpense, qianji.TypeIncome} {
		opts := []openrouter.Option{{Key: keep, Description: "用户没有提到要改分类"}}
		for _, cat := range limit(catalog.Leaves(kind), MaxChoiceOptions) {
			categoryKeys["c"+cat.ID] = cat.ID
			opts = append(opts, openrouter.Option{Key: "c" + cat.ID, Description: cat.Path})
		}
		name := "category_expense"
		if kind == qianji.TypeIncome {
			name = "category_income"
		}
		qs[name] = openrouter.ChoiceQuestion("按修改意见，这笔账应该记到哪个分类？", opts)
	}
	assetKeys := map[string]string{"none": ""}
	if len(catalog.Assets) > 0 {
		var opts []openrouter.Option
		for _, a := range limit(catalog.Assets, MaxChoiceOptions) {
			assetKeys["s"+a.ID] = a.ID
			opts = append(opts, openrouter.Option{Key: "s" + a.ID, Description: AssetLabel(a)})
		}
		head := []openrouter.Option{{Key: keep, Description: "用户没有提到要改这个账户"}, {Key: "none", Description: "不关联任何账户"}}
		qs["asset"] = openrouter.ChoiceQuestion("按修改意见，付款或收款（转账时为转出）用哪个账户？", append(head, opts...))
		qs["target_asset"] = openrouter.ChoiceQuestion("按修改意见，转账或还款时转入哪个账户？", append(head, opts...))
	}
	resp, err := r.Decider.Decide(ctx, openrouter.DecisionsRequest{Model: r.Config.Decision.Model, State: state, Questions: qs})
	if err != nil {
		return nil, tr, err
	}
	tr.Decision = resp
	tr.Cost += resp.Usage.Cost
	ans := resp.Answers
	out := *d
	out.Uncertain = nil
	if k := ans["kind"]; k.Choice != keep && k.Prob() >= 0.5 {
		out.Kind = Kind(k.Choice)
	}
	if out.Kind == KindExpense || out.Kind == KindIncome {
		catQ := "category_expense"
		if out.Kind == KindIncome {
			catQ = "category_income"
		}
		cat := ans[catQ]
		if c, ok := catalog.Category(out.CategoryID); cat.Choice != keep || !ok || c.Type != kindType(out.Kind) {
			// When the type changed, the old category no longer fits; take the
			// best non-keep option instead.
			if id := categoryKeys[bestExcept(cat, keep)]; id != "" {
				out.CategoryID = id
			}
		}
		out.TargetID = ""
	} else {
		out.CategoryID = ""
		if t, ok := ans["target_asset"]; ok && t.Choice != keep {
			out.TargetID = assetKeys[t.Choice]
		}
	}
	if a, ok := ans["asset"]; ok && a.Choice != keep {
		out.AssetID = assetKeys[a.Choice]
	}
	if ans["amount"].Noul >= 0.5 || ans["adjust"].Noul >= 0.5 || ans["time"].Noul >= 0.5 || ans["remark"].Noul >= 0.5 {
		fields, cost, err := r.correctFields(ctx, d, catalog, text, now)
		tr.LLMModel, tr.Cost = r.Config.Correction.Model, tr.Cost+cost
		if err != nil {
			return nil, tr, err
		}
		tr.LLM = fields
		if ans["amount"].Noul >= 0.5 && fields.Amount != "" {
			if m, err := amountIn(fields.Amount); err == nil && m != "0.00" {
				out.Money = m
				if cur := CurrencyCode(fields.Currency); cur != "" {
					out.Currency = cur
				}
			}
		}
		if ans["adjust"].Noul >= 0.5 {
			out.Discount = saidPart(fields.Discount, out.Discount)
			out.Fee = saidPart(fields.Fee, out.Fee)
		}
		if ans["time"].Noul >= 0.5 && fields.Time != "" {
			if t, err := time.ParseInLocation("2006-01-02 15:04", fields.Time, loc); err == nil {
				out.Time, out.TimeSource = t, "correction"
			}
		}
		if ans["remark"].Noul >= 0.5 && fields.Merchant != "" {
			out.Merchant = fields.Merchant
		}
	}
	return &out, tr, nil
}

// saidPart is the discount or fee the user gave, or was when they gave
// none; 0 removes it.
func saidPart(said, was string) string {
	m, err := amountIn(said)
	switch {
	case said == "" || err != nil:
		return was
	case m == "0.00":
		return ""
	}
	return m
}

func bestExcept(a openrouter.Answer, except string) string {
	if a.Choice != except {
		return a.Choice
	}
	keys := make([]string, 0, len(a.Probabilities))
	for k := range a.Probabilities {
		if k != except {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return a.Probabilities[keys[i]] > a.Probabilities[keys[j]] })
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

var correctionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"amount":   map[string]any{"type": "string", "description": "新的金额，两位小数；没提到则为空字符串"},
		"currency": map[string]any{"type": "string", "description": "新金额的币种代码，例如 USD、HKD；没说币种则为空字符串"},
		"discount": map[string]any{"type": "string", "description": "新的优惠金额，两位小数；说没有优惠时为 0；没提到则为空字符串"},
		"fee":      map[string]any{"type": "string", "description": "新的手续费，两位小数；说没有手续费时为 0；没提到则为空字符串"},
		"time":     map[string]any{"type": "string", "description": "新的时间，格式 YYYY-MM-DD HH:MM；没提到则为空字符串"},
		"merchant": map[string]any{"type": "string", "description": "新的备注或商户名称；没提到则为空字符串"},
	},
	"required":             []string{"amount", "currency", "discount", "fee", "time", "merchant"},
	"additionalProperties": false,
}

func (r *Recognizer) correctFields(ctx context.Context, d *Draft, catalog qianji.Catalog, text string, now time.Time) (*correctionFields, float64, error) {
	cfg := r.Config.Correction
	loc := r.Config.Location()
	prompt := fmt.Sprintf("用户刚记了一笔账：%s\n现在时间（北京时间）：%s\n用户的修改意见：%s\n\n从修改意见里提取新的金额（说了币种时一并提取）、优惠、手续费、时间和备注。时间按北京时间换算成绝对时间，只说了日期时保留原来的时分；没提到的字段返回空字符串。",
		Describe(d, catalog, loc), now.In(loc).Format("2006-01-02 15:04（Monday）"), text)
	req := openrouter.ChatRequest{
		Model:          cfg.Model,
		Messages:       []openrouter.Message{{Role: "user", Content: prompt}},
		MaxTokens:      cfg.MaxTokens,
		ResponseFormat: openrouter.JSONSchemaFormat("correction", correctionSchema),
		Provider:       map[string]any{"require_parameters": true},
	}
	if cfg.ReasoningEffort != "" {
		req.Reasoning = map[string]any{"effort": cfg.ReasoningEffort, "exclude": true}
	}
	var out correctionFields
	resp, err := r.Router.ChatJSON(ctx, req, &out)
	cost := 0.0
	if resp != nil {
		cost = resp.Usage.Cost
	}
	return &out, cost, err
}

var paymentLabel = regexp.MustCompile(`^(支付方式|付款方式|交易卡号|付款账户|收款账户|支付账户|扣款账户|到账账户|收款方式|付款卡)\s*[:：]?\s*`)

// PaymentText returns the value of the payment-method row, used as the key
// when the user teaches which asset a payment method belongs to.
func (t *Trace) PaymentText() string {
	rows := t.Rows
	if rows == nil {
		// Records from before the rows were kept.
		for _, row := range Rows(t.OCRLines) {
			rows = append(rows, row.Text)
		}
	}
	for _, text := range rows {
		if loc := paymentLabel.FindStringIndex(text); loc != nil {
			if v := strings.TrimSpace(text[loc[1]:]); v != "" {
				return v
			}
		}
	}
	return ""
}
