package recognize

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ledger-snap/internal/openrouter"
	"ledger-snap/internal/qianji"
)

var numberRe = regexp.MustCompile(`\d[\d,]*(?:\.\d+)?`)

// amountIn reads the first amount in a model's text, such as "¥1,200.5".
func amountIn(text string) (string, error) {
	return normalizeMoney(strings.ReplaceAll(numberRe.FindString(text), ",", ""))
}

// FallbackResult is the vision LLM's structured reading of the screenshot.
type FallbackResult struct {
	IsTransaction     bool   `json:"is_transaction"`
	IsFailed          bool   `json:"is_failed"`
	Kind              string `json:"kind"`
	Amount            string `json:"amount"`
	Currency          string `json:"currency"`
	ConvertedAmount   string `json:"converted_amount"`
	ConvertedCurrency string `json:"converted_currency"`
	Time              string `json:"time"`
	CategoryID        string `json:"category_id"`
	AssetID           string `json:"asset_id"`
	TargetAssetID     string `json:"target_asset_id"`
	Discount          string `json:"discount"`
	Fee               string `json:"fee"`
	Merchant          string `json:"merchant"`
	Reason            string `json:"reason"`
}

var fallbackSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"is_transaction":     map[string]any{"type": "boolean", "description": "截图是否展示了一笔具体的交易，已完成的和正在付款、即将付款的都算"},
		"is_failed":          map[string]any{"type": "boolean", "description": "交易是否已经失败、取消或关闭；待支付、确认付款、处理中都不算失败"},
		"kind":               map[string]any{"type": "string", "enum": []string{"expense", "income", "transfer", "credit", "refund", "none"}},
		"amount":             map[string]any{"type": "string", "description": "实际付款或到账金额，两位小数，不带符号；没有则为空字符串"},
		"currency":           map[string]any{"type": "string", "description": "amount 的币种代码，例如 CNY、USD、HKD；截图看不出币种则为空字符串"},
		"converted_amount":   map[string]any{"type": "string", "description": "同一笔交易换成另一种币种的金额，两位小数；没有则为空字符串"},
		"converted_currency": map[string]any{"type": "string", "description": "converted_amount 的币种代码；没有则为空字符串"},
		"time":               map[string]any{"type": "string", "description": "交易时间，格式 YYYY-MM-DD HH:MM:SS；截图里没有则为空字符串"},
		"category_id":        map[string]any{"type": "string", "description": "从给定分类中选择的分类 ID；无法判断则为空字符串"},
		"asset_id":           map[string]any{"type": "string", "description": "付款或收款账户 ID；自己账户之间转账或还款时为转出账户；看不出来则为空字符串"},
		"target_asset_id":    map[string]any{"type": "string", "description": "自己账户之间转账或还款时的转入账户 ID；其他情况为空字符串"},
		"discount":           map[string]any{"type": "string", "description": "这笔交易一共优惠的金额，两位小数；没有则为空字符串"},
		"fee":                map[string]any{"type": "string", "description": "自己账户之间转账或还款另外收的手续费、服务费，两位小数；没有则为空字符串"},
		"merchant":           map[string]any{"type": "string", "description": "交易对方，例如商户名称、收款方或付款人"},
		"reason":             map[string]any{"type": "string", "description": "一句话说明判断依据"},
	},
	"required":             []string{"is_transaction", "is_failed", "kind", "amount", "currency", "converted_amount", "converted_currency", "time", "category_id", "asset_id", "target_asset_id", "discount", "fee", "merchant", "reason"},
	"additionalProperties": false,
}

func catalogText(c qianji.Catalog) string {
	var b strings.Builder
	for _, kind := range []int{qianji.TypeExpense, qianji.TypeIncome} {
		label := "支出分类"
		if kind == qianji.TypeIncome {
			label = "收入分类"
		}
		b.WriteString(label + "（ID：名称）：\n")
		for _, cat := range c.Leaves(kind) {
			fmt.Fprintf(&b, "- %s：%s\n", cat.ID, cat.Path)
		}
	}
	b.WriteString("用户的账户（ID：名称）：\n")
	if len(c.Assets) == 0 {
		b.WriteString("（无）\n")
	}
	for _, a := range c.Assets {
		fmt.Fprintf(&b, "- %s：%s\n", a.ID, AssetLabel(a))
	}
	return b.String()
}

const fallbackInstructions = `你是记账助手。根据截图和 OCR 文字，判断这是不是一笔要记账的交易，并提取记账信息。
规则：
- 已经完成的交易要记，待支付、确认付款（包括 Apple Pay 等付款确认弹窗）、处理中的交易也要记；已经失败、取消或关闭的不记。
- kind：expense 支出（付款给商户或别人，包括给别人转账、发红包）；income 收入（收到别人的转账、红包、工资、收款）；transfer 用户自己两个账户之间转钱（提现到自己的银行卡、充值、转入余额宝等，转给别人不算）；credit 用自己的账户还自己的信用卡、花呗等；refund 退款；none 不是交易。
- 账单列表（多笔交易）、只显示余额的钱包页、商品页、行情和新闻都不是一笔具体的交易。
- amount 取优惠后的实付或实际到账金额，还没付款时取要付的金额，不要取原价、优惠金额、余额或订单号。自己账户之间转账或还款时，amount 取转出账户实际扣掉的金额：有手续费时包含手续费，有优惠时是优惠后的金额。
- discount 取这笔交易本身一共优惠的金额（立减、满减、优惠券、红包或积分抵扣、还款优惠）；广告、付款后领到的券不算。fee 取转账或还款另外收的手续费、服务费；写着免费时留空。
- 换汇或跨币种转账时 amount 取卖出、转出的金额，converted_amount 取买入、转入的金额；外币消费同时显示原币和折算金额时，amount 取原币金额，converted_amount 取账户实际扣款或折合的金额。只有一种币种时 converted_amount 留空。
- time 取交易发生的时间，不要用顶部状态栏的时钟；只有月日时，年份按上传时间推断。
- category_id 只能从给出的、与 kind 对应的分类里选 ID。
- asset_id 只能从给出的账户里选 ID，而且截图里的支付方式、付款方式或收款账户要和账户名称或备注（含卡号尾号）明显对应；对应不上就留空。转账和还款时 asset_id 是转出账户，target_asset_id 是转入账户；零钱提现时转出的是零钱本身，到账的银行卡是转入账户。
- 只输出 JSON。`

func (r *Recognizer) fallback(ctx context.Context, rows []Row, in Input, image []byte, trace *Trace) (*Draft, error) {
	cfg := r.Config.Fallback
	trace.FallbackModel = cfg.Model
	text := fmt.Sprintf("%s\n\nOCR 文字（从上到下）：\n%s\n上传时间（北京时间）：%s\n\n%s",
		fallbackInstructions, rowsText(rows), in.UploadTime.In(r.Config.Location()).Format("2006-01-02 15:04:05"), catalogText(in.Catalog))
	if h := r.historyText(in.past, in.Catalog); h != "" {
		text += "\n" + h
	}
	content := []any{map[string]any{"type": "text", "text": text}}
	if image != nil {
		content = append(content, openrouter.ImagePart(image))
	}
	req := openrouter.ChatRequest{
		Model:          cfg.Model,
		Messages:       []openrouter.Message{{Role: "user", Content: content}},
		MaxTokens:      cfg.MaxTokens,
		ResponseFormat: openrouter.JSONSchemaFormat("bill", fallbackSchema),
		Provider:       map[string]any{"require_parameters": true},
	}
	if cfg.ReasoningEffort != "" {
		req.Reasoning = map[string]any{"effort": cfg.ReasoningEffort, "exclude": true}
	}
	var out FallbackResult
	start := time.Now()
	resp, err := r.Router.ChatJSON(ctx, req, &out)
	trace.FallbackMs = time.Since(start).Milliseconds()
	if resp != nil {
		trace.Cost += resp.Usage.Cost
	}
	if err != nil {
		return nil, err
	}
	trace.Fallback = &out
	return r.fromFallback(out, in), nil
}

func (r *Recognizer) fromFallback(f FallbackResult, in Input) *Draft {
	if !f.IsTransaction || f.Kind == string(KindNone) {
		return reject("截图不是交易记录")
	}
	if f.IsFailed {
		return reject("截图里的交易已经失败或取消")
	}
	money, err := amountIn(f.Amount)
	if err != nil || money == "0.00" {
		return reject("没有找到交易金额")
	}
	d := &Draft{Status: "ok", Kind: Kind(f.Kind), Money: money, Merchant: strings.TrimSpace(f.Merchant), Source: "fallback", Currency: CurrencyCode(f.Currency)}
	if conv, err := amountIn(f.ConvertedAmount); err == nil && conv != "0.00" {
		if cur := CurrencyCode(f.ConvertedCurrency); conv != money || cur != d.Currency {
			d.Converted, d.ConvertedCurrency = conv, cur
		}
	}
	// The amount is already what left the paying account (see adjust).
	if d.Converted == "" && (d.Kind == KindExpense || d.Kind.IsTransfer()) {
		part := func(s string) string {
			v, err := amountIn(s)
			if err != nil || toCents(v) == 0 || toCents(v) >= toCents(money) {
				return ""
			}
			return v
		}
		d.Discount = part(f.Discount)
		if d.Kind.IsTransfer() {
			d.Fee = part(f.Fee)
		}
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", f.Time, r.Config.Location()); err == nil {
		d.Time, d.TimeSource = t, "screenshot"
	}
	if c, ok := in.Catalog.Category(f.CategoryID); ok && c.Type == kindType(d.Kind) {
		d.CategoryID = c.ID
	} else {
		d.mark("category")
	}
	if a, ok := in.Catalog.Asset(f.AssetID); ok {
		d.AssetID = a.ID
	}
	if d.Kind.IsTransfer() {
		d.CategoryID = ""
		d.Uncertain = remove(d.Uncertain, "category")
		if a, ok := in.Catalog.Asset(f.TargetAssetID); ok && a.ID != d.AssetID {
			d.TargetID = a.ID
		}
	}
	return d
}
