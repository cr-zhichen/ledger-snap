package app

import (
	"strings"
	"time"

	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
)

// BillDisplay is composed from the current draft, never from a saved summary.
// It is only used by the shortcut flow; the record API keeps its compact summary.
type BillDisplay struct {
	Amount, Merchant, Details string
}

func billDisplay(d *recognize.Draft, c qianji.Catalog, loc *time.Location) BillDisplay {
	amount := strings.TrimSpace(d.Money + " " + d.Currency)
	if m, err := moneyOf(d, c); err == nil {
		amount = m.Text
	}
	// moneyOf already formats conversions, estimated amounts and adjustments.
	// Move its adjustment annotation to a separate line without recalculating it.
	amount, notes, _ := strings.Cut(amount, "（")
	if len(strings.Fields(amount)) == 1 {
		if c.Base() == "CNY" {
			amount = "¥" + amount
		} else {
			amount += " " + c.Base()
		}
	}
	mark := func(field, label string) string {
		for _, f := range d.Uncertain {
			if f == field {
				return label + "?"
			}
		}
		return label
	}
	asset := func(id, empty string) string {
		if a, ok := c.Asset(id); ok {
			return a.Name
		}
		return empty
	}
	merchant := d.Merchant
	if merchant == "" {
		merchant = d.Kind.Label()
	}
	lines := []string{merchant}
	if d.Kind.IsTransfer() {
		lines = append(lines, mark("asset", asset(d.AssetID, "未选转出账户"))+" → "+mark("target_asset", asset(d.TargetID, "未选转入账户")))
	} else {
		category := "未分类"
		if cat, ok := c.Category(d.CategoryID); ok {
			category = cat.Path
		}
		lines = append(lines, mark("category", category)+" · "+mark("asset", asset(d.AssetID, "无账户")))
	}
	lines = append(lines, mark("time", d.Time.In(loc).Format("01-02 15:04")))
	if notes != "" {
		lines = append(lines, strings.Split(strings.TrimSuffix(notes, "）"), "，")...)
	}
	return BillDisplay{Amount: amount, Merchant: merchant, Details: strings.Join(lines, "\n")}
}
