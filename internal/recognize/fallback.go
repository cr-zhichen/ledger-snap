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

// FallbackResult is the vision LLM's structured reading of the screenshot.
type FallbackResult struct {
	IsTransaction bool   `json:"is_transaction"`
	IsSuccess     bool   `json:"is_success"`
	Kind          string `json:"kind"`
	Amount        string `json:"amount"`
	Time          string `json:"time"`
	CategoryID    string `json:"category_id"`
	AssetID       string `json:"asset_id"`
	TargetAssetID string `json:"target_asset_id"`
	Merchant      string `json:"merchant"`
	Reason        string `json:"reason"`
}

var fallbackSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"is_transaction":  map[string]any{"type": "boolean", "description": "截图是否展示了一笔具体的交易"},
		"is_success":      map[string]any{"type": "boolean", "description": "交易是否已经成功完成"},
		"kind":            map[string]any{"type": "string", "enum": []string{"expense", "income", "transfer", "credit", "refund", "none"}},
		"amount":          map[string]any{"type": "string", "description": "实际付款或到账金额，两位小数，不带符号；没有则为空字符串"},
		"time":            map[string]any{"type": "string", "description": "交易时间，格式 YYYY-MM-DD HH:MM:SS；截图里没有则为空字符串"},
		"category_id":     map[string]any{"type": "string", "description": "从给定分类中选择的分类 ID；无法判断则为空字符串"},
		"asset_id":        map[string]any{"type": "string", "description": "付款或收款账户 ID；自己账户之间转账或还款时为转出账户；看不出来则为空字符串"},
		"target_asset_id": map[string]any{"type": "string", "description": "自己账户之间转账或还款时的转入账户 ID；其他情况为空字符串"},
		"merchant":        map[string]any{"type": "string", "description": "交易对方，例如商户名称、收款方或付款人"},
		"reason":          map[string]any{"type": "string", "description": "一句话说明判断依据"},
	},
	"required":             []string{"is_transaction", "is_success", "kind", "amount", "time", "category_id", "asset_id", "target_asset_id", "merchant", "reason"},
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

const fallbackInstructions = `你是记账助手。根据截图和 OCR 文字，判断这是不是一笔已经完成的交易，并提取记账信息。
规则：
- kind：expense 支出（付款给商户或别人，包括给别人转账、发红包）；income 收入（收到别人的转账、红包、工资、收款）；transfer 用户自己两个账户之间转钱（提现到自己的银行卡、充值、转入余额宝等，转给别人不算）；credit 用自己的账户还自己的信用卡、花呗等；refund 退款；none 不是交易。
- 账单列表（多笔交易）、只显示余额的钱包页、商品页、待支付页、行情和新闻都不是一笔已完成的交易。
- amount 取优惠后的实付或实际到账金额，不要取原价、优惠金额、余额或订单号。
- time 取交易发生的时间，不要用顶部状态栏的时钟；只有月日时，年份按上传时间推断。
- category_id 只能从给出的、与 kind 对应的分类里选 ID。
- asset_id 只能从给出的账户里选 ID，而且截图里的支付方式、付款方式或收款账户要和账户名称或备注（含卡号尾号）明显对应；对应不上就留空。转账和还款时 asset_id 是转出账户，target_asset_id 是转入账户；零钱提现时转出的是零钱本身，到账的银行卡是转入账户。
- 只输出 JSON。`

func (r *Recognizer) fallback(ctx context.Context, rows []Row, in Input, image []byte, trace *Trace) (*Draft, error) {
	cfg := r.Config.Fallback
	trace.FallbackModel = cfg.Model
	text := fmt.Sprintf("%s\n\nOCR 文字（从上到下）：\n%s\n上传时间（北京时间）：%s\n\n%s",
		fallbackInstructions, rowsText(rows), in.UploadTime.In(r.Config.Location()).Format("2006-01-02 15:04:05"), catalogText(in.Catalog))
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
	if !f.IsSuccess {
		return reject("截图里的交易还没有完成")
	}
	money, err := normalizeMoney(strings.ReplaceAll(numberRe.FindString(f.Amount), ",", ""))
	if err != nil || money == "0.00" {
		return reject("没有找到交易金额")
	}
	d := &Draft{Status: "ok", Kind: Kind(f.Kind), Money: money, Merchant: strings.TrimSpace(f.Merchant), Source: "fallback"}
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
