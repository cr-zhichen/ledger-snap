package recognize

import (
	"fmt"
	"ledger-snap/internal/qianji"
	"strings"
	"time"
)

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
