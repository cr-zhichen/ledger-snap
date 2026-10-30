package recognize

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Fact contains visible transaction information, never account/category IDs.
type Fact struct {
	Kind              Kind     `json:"kind"`
	Amount            string   `json:"amount"`
	Received          string   `json:"same_currency_received"`
	AmountRole        string   `json:"amount_role"`
	Currency          string   `json:"currency"`
	Converted         string   `json:"converted_amount"`
	ConvertedCurrency string   `json:"converted_currency"`
	Discount          string   `json:"discount"`
	Fee               string   `json:"fee"`
	Date              string   `json:"date"`
	Clock             string   `json:"clock"`
	Merchant          string   `json:"merchant"`
	Payment           string   `json:"payment"`
	TargetPayment     string   `json:"target_payment"`
	Description       string   `json:"description"`
	Uncertain         []string `json:"uncertain_fields"`
}
type Extraction struct {
	Transactions []Fact `json:"transactions"`
	Reason       string `json:"reason"`
}

const extractionInstructions = `你是记账截图识别助手。只根据图片提取交易事实，按从上到下输出 transactions 数组，不选择钱迹账户或分类。
- 支持单笔和多笔交易列表。每个完整交易卡片输出一项；月度统计、余额、广告、导航、推荐商品不算交易。金额被遮住或卡片只露出边缘时不猜测。不是交易则返回空数组并在 reason 说明。
- 已完成、待支付、确认付款（含 Apple Pay）、处理中的具体交易都提取；失败、取消、关闭的不要提取。通知中明确的支出或收入算交易；验证码短信不算交易。
- kind: expense 支出（消费、转给别人、发红包），income 收入，transfer 自己账户转账或提现充值，credit 还信用卡花呗，refund 退款。transfer 仅限明确的自己账户之间转移；给具名收款人转账、向别人付款都用 expense，即使页面标题叫“转账成功”。
- amount 取这笔交易的实际支付或到账金额，两位小数不带符号，不能取广告价、优惠、余额或订单号。amount_role: paid 表示实付或源账户实际扣款；principal 表示尚未计入手续费或优惠的本金；received 表示转账或还款的目标账户到账额。支出和收入一般为 paid。转账优先选明确的实际扣款，不要自己加减；只显示提现本金和额外手续费时输出本金及 principal，只有到账额时输出 received。
- same_currency_received 只填自己账户转账/还款截图明确显示的同币种实际到账金额，没有则空；与 amount 分别原样提取，不要计算。例如提现本金 500、服务费 0.50、实际到账 499.50，必须保留到账 499.50，以便程序判断手续费是内扣还是额外收取。
- discount 是这笔交易已使用的优惠合计，有合计直接取，不能把合计再加到明细上。fee 是自己转账/提现/还款额外收取的手续费。没使用的付款方式优惠、付款后领取的券、积分奖励不是已用优惠。
- currency 为 amount 的币种代码（CNY/USD/HKD 等），单独显示的币种也要关联；卡片装饰上的 HKD/RMB 不是交易币种。不明确时为空。
- converted_amount/converted_currency 仅在截图明确显示同笔交易的另一币种金额时填写；换汇的 amount 取卖出金额，converted_amount 取买入金额，不估算汇率。外币消费的 amount/currency 必须用原交易金额（Amount/Transaction amount），converted_amount/converted_currency 用入账金额（Billed amount/Settled amount），不能反过来。
- date 只表示交易日期：明确年月日输出 YYYY-MM-DD；只有月日输出 MM-DD；今天/昨天分别输出 today/yesterday；没有日期留空。clock 只写交易的 HH:MM 或 HH:MM:SS，没有时分留空。不能取顶部状态栏、倒计时、配送时间或订阅结束日期。列表每笔旁的时分是该笔交易时间，可以提取。
- merchant 优先用明确的交易对象/交易商户，再用该笔商户标题；不要把“交易对象”等标签放进名称。
- payment 原样提取当前选中的付款/收款方式和卡号尾号，保留银行或平台名称。收入的到账账户也放 payment，target_payment 只用于自己账户转账或还款的转入账户。Apple Pay 用当前展示的卡，不用背景未选卡。支付宝通知不等于使用支付宝余额。转账/还款的 payment 是转出，target_payment 是转入。“零钱提现”的转出是微信零钱，不能填到账银行卡；已领取的微信红包到账微信零钱。
- description 简述该笔商品或服务，供后续分类；不推测看不到的商品。各笔不能混用金额、账户或日期。
- 不清楚的事实留空，并把对应字段写进 uncertain_fields，允许 money、currency、time、discount、fee。不要输出猜测的金额。只输出 JSON。`

func factSchema() map[string]any {
	props := map[string]any{}
	for _, k := range []string{"amount", "currency", "converted_amount", "converted_currency", "same_currency_received", "discount", "fee", "date", "clock", "merchant", "payment", "target_payment", "description"} {
		props[k] = map[string]any{"type": "string"}
	}
	props["kind"] = map[string]any{"type": "string", "enum": []string{"expense", "income", "transfer", "credit", "refund"}}
	props["amount_role"] = map[string]any{"type": "string", "enum": []string{"paid", "principal", "received"}}
	props["uncertain_fields"] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"money", "currency", "time", "discount", "fee"}}}
	return map[string]any{"type": "object", "properties": props, "required": []string{"kind", "amount", "amount_role", "currency", "converted_amount", "converted_currency", "same_currency_received", "discount", "fee", "date", "clock", "merchant", "payment", "target_payment", "description", "uncertain_fields"}, "additionalProperties": false}
}

var extractionSchema = map[string]any{"type": "object", "properties": map[string]any{
	"transactions": map[string]any{"type": "array", "items": factSchema()}, "reason": map[string]any{"type": "string"},
}, "required": []string{"transactions", "reason"}, "additionalProperties": false}

func (r *Recognizer) draft(f Fact, upload time.Time) (*Draft, error) {
	if !slices.Contains([]Kind{KindExpense, KindIncome, KindTransfer, KindCredit, KindRefund}, f.Kind) {
		return nil, fmt.Errorf("无法确定交易类型")
	}
	money, err := amountIn(f.Amount)
	if err != nil || toCents(money) == 0 {
		return nil, fmt.Errorf("无法确定交易金额，请换一张完整截图")
	}
	if !slices.Contains([]string{"paid", "principal", "received"}, f.AmountRole) {
		return nil, fmt.Errorf("无法确定金额是否包含手续费或优惠")
	}
	d := &Draft{Status: "ok", Kind: f.Kind, Money: money, Merchant: strings.TrimSpace(f.Merchant), Payment: strings.TrimSpace(f.Payment), TargetPayment: strings.TrimSpace(f.TargetPayment), Description: strings.TrimSpace(f.Description)}
	for _, pair := range []struct {
		raw string
		dst *string
	}{{f.Currency, &d.Currency}, {f.ConvertedCurrency, &d.ConvertedCurrency}} {
		*pair.dst = CurrencyCode(pair.raw)
		if pair.raw != "" && *pair.dst == "" {
			return nil, fmt.Errorf("无法确定币种")
		}
	}
	for _, pair := range []struct {
		raw string
		dst *string
	}{{f.Discount, &d.Discount}, {f.Fee, &d.Fee}, {f.Converted, &d.Converted}} {
		if pair.raw == "" {
			continue
		}
		value, err := amountIn(pair.raw)
		if err != nil {
			return nil, err
		}
		if toCents(value) > 0 {
			*pair.dst = value
		}
	}
	if (d.Converted == "") != (d.ConvertedCurrency == "") {
		return nil, fmt.Errorf("另一币种金额和币种不完整")
	}
	if d.Kind == KindIncome || d.Kind == KindRefund {
		d.Discount, d.Fee = "", ""
	} else if !d.Kind.IsTransfer() {
		d.Fee = ""
	}
	if d.Converted != "" {
		if f.AmountRole != "paid" {
			return nil, fmt.Errorf("请提供显示转出金额的截图")
		}
		d.Discount, d.Fee = "", ""
	} else {
		received := int64(0)
		if f.Received != "" {
			value, err := amountIn(f.Received)
			if err != nil {
				return nil, err
			}
			received = toCents(value)
		}
		paid := toCents(d.Money)
		if d.Kind.IsTransfer() && received > 0 {
			expected := received + toCents(d.Fee) - toCents(d.Discount)
			if f.AmountRole == "paid" && paid != expected {
				d.mark("money")
			} else {
				paid = expected
			}
		} else if f.AmountRole != "paid" {
			paid -= toCents(d.Discount)
			if d.Kind.IsTransfer() {
				paid += toCents(d.Fee)
			}
		}
		if paid <= 0 {
			return nil, fmt.Errorf("优惠与金额不符")
		}
		d.Money = fromCents(paid)
	}

	when, source, err := transactionTime(f.Date, f.Clock, upload, r.Config.Location())
	if err != nil {
		return nil, err
	}
	d.Time, d.TimeSource = when, source
	for _, field := range f.Uncertain {
		if !slices.Contains([]string{"money", "currency", "time", "discount", "fee"}, field) {
			return nil, fmt.Errorf("无法确认的字段无效")
		}
		d.mark(field)
	}
	r.finishTime(d, Input{UploadTime: upload})
	return d, nil
}

func transactionTime(date, clock string, upload time.Time, loc *time.Location) (time.Time, string, error) {
	now := upload.In(loc)
	day := now
	source := "screenshot"
	switch date {
	case "", "today":
	case "yesterday":
		day = now.AddDate(0, 0, -1)
	default:
		var err error
		if len(date) == 5 {
			day, err = time.ParseInLocation("2006-01-02", fmt.Sprintf("%d-%s", now.Year(), date), loc)
			if err == nil && day.After(now) {
				day = day.AddDate(-1, 0, 0)
			}
		} else {
			day, err = time.ParseInLocation("2006-01-02", date, loc)
		}
		if err != nil {
			return time.Time{}, "", fmt.Errorf("交易日期无效，请确认日期")
		}
	}
	if clock == "" {
		if date == "" {
			return now, "upload", nil
		}
		clock = "12:00:00"
		if day.Format("2006-01-02") == now.Format("2006-01-02") {
			clock = now.Format("15:04:05")
		}
	}
	layout := "2006-01-02 15:04"
	if len(clock) == 8 {
		layout = "2006-01-02 15:04:05"
	}
	result, err := time.ParseInLocation(layout, day.Format("2006-01-02")+" "+clock, loc)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("交易时间无效，请确认时间")
	}
	return result, source, nil
}
