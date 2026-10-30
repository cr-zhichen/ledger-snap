package recognize

import (
	"context"
	"slices"
	"time"

	"ledger-snap/internal/qianji"
)

type Kind string

const (
	KindExpense  Kind = "expense"
	KindIncome   Kind = "income"
	KindTransfer Kind = "transfer"
	KindCredit   Kind = "credit"
	KindRefund   Kind = "refund"
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
	// Currency is the code of Money; "" means the account's currency. For a
	// transfer, Money is the amount sent.
	Currency string `json:"currency,omitempty"`
	// Converted is the same bill in another currency, when the screenshot
	// shows one: what a currency exchange bought, or what a foreign purchase
	// cost in the card's currency. "" currency means the other account's.
	Converted         string `json:"converted,omitempty"`
	ConvertedCurrency string `json:"converted_currency,omitempty"`
	Payment           string `json:"payment,omitempty"`
	TargetPayment     string `json:"target_payment,omitempty"`
	Description       string `json:"description,omitempty"`
	CategoryHint      string `json:"category_hint,omitempty"`
	// Discount is what the user saved, "" for none. For an expense Money is
	// already what was paid; for a transfer or repayment the target account
	// receives Money plus the discount.
	Discount string `json:"discount,omitempty"`
	// Fee is what a transfer or repayment cost on top of what the target
	// account received; Money includes it. "" for none.
	Fee string `json:"fee,omitempty"`
}

func (d *Draft) mark(field string) {
	if !slices.Contains(d.Uncertain, field) {
		d.Uncertain = append(d.Uncertain, field)
	}
}

// Memory holds learned rules: payment text to asset.
type Memory struct {
	AssetAliases map[string]string
}

// PastBill is one of the user's earlier bills with a merchant.
type PastBill struct {
	Time       time.Time `json:"time"`
	Kind       Kind      `json:"kind"` // KindExpense or KindIncome
	Money      string    `json:"money"`
	Currency   string    `json:"currency,omitempty"` // "" is the account's
	CategoryID string    `json:"category_id"`
	AssetID    string    `json:"asset_id,omitempty"`
}

// History looks up the user's recent bills with each merchant, newest first.
type History func(ctx context.Context, merchants []string) (map[string][]PastBill, error)

// AssetLabel names an asset with its remark, e.g. "工行卡（储蓄卡 尾号5678）".
func AssetLabel(a qianji.Asset) string {
	if a.Remark != "" {
		return a.Name + "（" + a.Remark + "）"
	}
	return a.Name
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
