package recognize

import (
	"context"
	"fmt"
	"time"

	"ledger-snap/internal/qianji"
)

// Null leaves a field untouched; an empty string explicitly clears it.
type correctionFields struct {
	Kind          *Kind   `json:"kind"`
	Amount        *string `json:"amount"`
	Currency      *string `json:"currency"`
	Discount      *string `json:"discount"`
	Fee           *string `json:"fee"`
	Date          *string `json:"date"`
	Clock         *string `json:"clock"`
	Merchant      *string `json:"merchant"`
	Payment       *string `json:"payment"`
	TargetPayment *string `json:"target_payment"`
	Category      *string `json:"category"`
}

func correctionSchema() map[string]any {
	names := []string{"kind", "amount", "currency", "discount", "fee", "date", "clock", "merchant", "payment", "target_payment", "category"}
	props := map[string]any{}
	for _, name := range names {
		props[name] = map[string]any{"type": []string{"string", "null"}}
	}
	return map[string]any{"type": "object", "properties": props, "required": names, "additionalProperties": false}
}

// Correct extracts only explicit edits, then uses the same matching stage.
func (r *Recognizer) Correct(ctx context.Context, d *Draft, catalog qianji.Catalog, text string, now time.Time) (*Draft, *Trace, error) {
	trace := &Trace{ExtractionModel: r.Config.Extraction.Model}
	start := time.Now()
	defer func() { trace.TotalMs = time.Since(start).Milliseconds() }()
	prompt := fmt.Sprintf(`按用户意见提取要修改的字段，未提到的字段必须为 null，不要重新生成整笔账。
原账单：%s。现在时间：%s。用户意见：%s。
kind 使用 expense/income/transfer/credit/refund。amount 是实际扣款/到账金额，保留两位小数。discount/fee 填 0 清除。
date 输出 YYYY-MM-DD，clock 输出 HH:MM:SS；只改日期时 clock 为 null，只改时分时 date 为 null。category 放用户指定的分类名称。
payment/target_payment 放用户说的付款/转入账户名称，明确不关联账户时填空字符串。只输出 JSON。`, Describe(d, catalog, r.Config.Location()), now.In(r.Config.Location()).Format(time.RFC3339), text)
	var changes correctionFields
	response, err := r.Router.ChatJSON(ctx, extractionRequest(r.Config.Extraction, "correction", correctionSchema(), prompt, nil), &changes)
	trace.ExtractionMs = time.Since(start).Milliseconds()
	if response != nil {
		trace.Cost += response.Usage.Cost
	}
	if err != nil {
		return nil, trace, err
	}
	when := d.Time.In(r.Config.Location())
	fact := Fact{Kind: d.Kind, Amount: d.Money, AmountRole: "paid", Currency: d.Currency, Converted: d.Converted, ConvertedCurrency: d.ConvertedCurrency, Discount: d.Discount, Fee: d.Fee, Date: when.Format("2006-01-02"), Clock: when.Format("15:04:05"), Merchant: d.Merchant, Payment: d.Payment, TargetPayment: d.TargetPayment, Description: d.Description}
	if changes.Kind != nil {
		fact.Kind = *changes.Kind
	}
	for _, field := range []struct {
		value  *string
		target *string
	}{{changes.Amount, &fact.Amount}, {changes.Currency, &fact.Currency}, {changes.Discount, &fact.Discount}, {changes.Fee, &fact.Fee}, {changes.Date, &fact.Date}, {changes.Clock, &fact.Clock}, {changes.Merchant, &fact.Merchant}, {changes.Payment, &fact.Payment}, {changes.TargetPayment, &fact.TargetPayment}} {
		if field.value != nil {
			*field.target = *field.value
		}
	}
	if d.Converted != "" && (changes.Discount != nil || changes.Fee != nil) {
		return nil, trace, fmt.Errorf("跨币种交易请直接修改实际金额")
	}
	next, err := r.draft(fact, now)
	if err != nil {
		return nil, trace, err
	}
	next.Uncertain = append([]string{}, d.Uncertain...)
	for _, f := range []struct {
		changed bool
		name    string
	}{{changes.Amount != nil, "money"}, {changes.Currency != nil, "currency"}, {changes.Discount != nil, "discount"}, {changes.Fee != nil, "fee"}, {changes.Date != nil || changes.Clock != nil, "time"}} {
		if f.changed {
			next.Uncertain = remove(next.Uncertain, f.name)
		}
	}
	matchCategory := changes.Category != nil || next.Kind != d.Kind
	matchAccounts := changes.Payment != nil || changes.TargetPayment != nil || next.Kind != d.Kind
	if changes.Category != nil {
		next.CategoryHint = *changes.Category
	}
	if matchCategory || matchAccounts {
		if err := r.Match(ctx, []*Draft{next}, Input{Catalog: catalog, UploadTime: now}, trace); err != nil {
			return nil, trace, err
		}
	}
	if !matchCategory {
		next.CategoryID = d.CategoryID
		next.Uncertain = restoreUncertain(next.Uncertain, d.Uncertain, "category")
	}
	if changes.Payment == nil && next.Kind == d.Kind {
		next.AssetID = d.AssetID
		next.Uncertain = restoreUncertain(next.Uncertain, d.Uncertain, "asset")
	} else if changes.Payment != nil && *changes.Payment == "" {
		next.AssetID = ""
		next.Uncertain = remove(next.Uncertain, "asset")
	}
	if changes.TargetPayment == nil && next.Kind == d.Kind {
		next.TargetID = d.TargetID
		next.Uncertain = restoreUncertain(next.Uncertain, d.Uncertain, "target_asset")
	} else if changes.TargetPayment != nil && *changes.TargetPayment == "" {
		next.TargetID = ""
		next.Uncertain = remove(next.Uncertain, "target_asset")
	}
	finishCurrency(next, catalog)
	return next, trace, nil
}
func restoreUncertain(current, previous []string, field string) []string {
	current = remove(current, field)
	for _, f := range previous {
		if f == field {
			return append(current, field)
		}
	}
	return current
}
