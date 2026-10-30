package app

import (
	"cmp"
	"fmt"
	"math"
	"strconv"
	"strings"

	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
)

// billMoney is what Qianji stores for a draft's amount, worked out the way
// the app's currency dialog does.
type billMoney struct {
	// Value is the bill's money, in the paying account's currency; for a
	// transfer, what left the source account.
	Value    string
	Currency *qianji.Currency // nil when only the base currency is involved
	// Estimated reports that an amount that moves an account was converted
	// with Qianji's prices because the screenshot does not show it.
	Estimated bool
	// Other is the amount on the other side of a two-currency bill, in
	// OtherCurrency: what a transfer's target account received, or what a
	// foreign purchase cost in the account's currency.
	Other, OtherCurrency string
	// Transfee is the bill's extra.transfee: a fee, or a discount as a
	// negative number; "" for neither.
	Transfee string
	Text     string // for the summary, e.g. "3940.45 HKD → 500.00 USD"
}

type amount struct {
	currency string
	value    float64
}

// moneyOf works out the bill amounts for a draft. The draft's amounts are
// taken as shown; an amount the screenshot does not show is converted with
// Qianji's prices. OtherCurrency is set even when that fails, so the user can
// type the amount instead.
func moneyOf(d *recognize.Draft, c qianji.Catalog) (billMoney, error) {
	base := c.Base()
	v, err := strconv.ParseFloat(d.Money, 64)
	if err != nil {
		return billMoney{}, fmt.Errorf("金额不对：%s", d.Money)
	}
	first := amount{d.Currency, v}
	var second *amount
	if w, err := strconv.ParseFloat(d.Converted, 64); err == nil && w > 0 {
		second = &amount{d.ConvertedCurrency, w}
	}
	estimated := false
	// value returns the amount in a currency: shown in the screenshot, or
	// converted from the first amount.
	value := func(cur string, moves bool) (float64, error) {
		if first.currency == cur {
			return first.value, nil
		}
		if second != nil && second.currency == cur {
			return second.value, nil
		}
		w, ok := c.Convert(first.value, first.currency, cur)
		if !ok {
			return 0, fmt.Errorf("钱迹没有 %s 兑 %s 的汇率，请改%s", first.currency, cur, OtherLabel(d.Kind))
		}
		estimated = estimated || moves
		return w, nil
	}

	if d.Kind.IsTransfer() {
		// Until the accounts are picked, take the amounts as shown.
		from, ok := c.AssetCurrency(d.AssetID)
		if !ok {
			from = cmp.Or(first.currency, base)
		}
		to, ok := c.AssetCurrency(d.TargetID)
		if !ok {
			to = from
			if second != nil && second.currency != "" {
				to = second.currency
			}
		}
		if first.currency == "" {
			first.currency = from
		}
		if second != nil && second.currency == "" {
			second.currency = to
		}
		if second != nil && first.currency != from && second.currency == from {
			first, *second = *second, first
		}
		var m billMoney
		if to != from {
			m.OtherCurrency = to
		}
		sent, err := value(from, true)
		if err != nil {
			return m, err
		}
		received := sent
		if to != from {
			if received, err = value(to, true); err != nil {
				return m, err
			}
		}
		m.Value, m.Text = money(sent), money(sent)
		if from == base && to == base {
			return m, adjust(&m, d, sent)
		}
		m.Currency = &qianji.Currency{Source: from, SourceValue: money(sent), Target: to, TargetValue: money(received)}
		if from != base {
			b := received
			if to != base {
				if b, err = value(base, false); err != nil {
					return m, err
				}
			}
			m.Currency.Base, m.Currency.BaseValue = base, money(b)
		}
		m.Text = money(sent) + " " + from
		if to != from {
			m.Other = money(received)
			m.Text += " → " + approx(estimated) + money(received) + " " + to
		}
		m.Estimated = estimated
		return m, nil
	}

	account, hasAccount := c.AssetCurrency(d.AssetID)
	if first.currency == "" {
		first.currency = base
		if hasAccount {
			first.currency = account
		}
	}
	if second != nil && second.currency == "" {
		// An unlabeled converted amount is the account's.
		second.currency = account
		if !hasAccount || first.currency == account {
			second = nil
		}
	}
	// What was paid is the foreign side; the account's side is what it cost.
	if hasAccount && second != nil && first.currency == account && second.currency != account {
		first, *second = *second, first
	}
	paid := first
	if !hasAccount || paid.currency == account {
		m := billMoney{Value: money(paid.value), Text: money(paid.value)}
		if paid.currency != base {
			b, err := value(base, false)
			if err != nil {
				return billMoney{}, err
			}
			m.Currency = &qianji.Currency{Source: paid.currency, SourceValue: money(paid.value), Base: base, BaseValue: money(b)}
			m.Text += " " + paid.currency
			return m, nil
		}
		return m, adjust(&m, d, paid.value)
	}
	m := billMoney{OtherCurrency: account}
	cost, err := value(account, true)
	if err != nil {
		return m, err
	}
	b, err := value(base, false)
	if err != nil {
		return m, err
	}
	m.Value, m.Other, m.Estimated = money(cost), money(cost), estimated
	m.Currency = &qianji.Currency{
		Source: paid.currency, SourceValue: money(paid.value),
		Target: account, TargetValue: money(cost),
		Base: base, BaseValue: money(b),
	}
	m.Text = money(paid.value) + " " + paid.currency + " = " + approx(estimated) + money(cost) + " " + account
	return m, nil
}

// adjust applies a draft's discount and, for a transfer or repayment, its
// fee to a bill in the base currency. Qianji keeps the amount without the fee
// and moves the accounts by extra.transfee on top of it: a fee out of the
// source account, a discount into the target account.
// sent is what left the paying account.
func adjust(m *billMoney, d *recognize.Draft, sent float64) error {
	var fee, discount float64
	if d.Kind.IsTransfer() {
		fee, _ = strconv.ParseFloat(d.Fee, 64)
	}
	if d.Kind == recognize.KindExpense || d.Kind.IsTransfer() {
		discount, _ = strconv.ParseFloat(d.Discount, 64)
	}
	if fee <= 0 && discount <= 0 {
		return nil
	}
	t := fee - discount
	if sent-max(t, 0) < 0.005 {
		return fmt.Errorf("手续费 %s 不比金额 %s 少，请改金额或手续费", d.Fee, d.Money)
	}
	m.Value, m.Transfee = money(sent-max(t, 0)), money(t)
	var notes []string
	if fee > 0 {
		notes = append(notes, "手续费 "+money(fee))
	}
	if discount > 0 {
		notes = append(notes, "优惠 "+money(discount))
	}
	if d.Kind.IsTransfer() {
		notes = append(notes, "到账 "+money(sent-t))
	}
	m.Text += "（" + strings.Join(notes, "，") + "）"
	return nil
}

func money(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64) }

func approx(estimated bool) string {
	if estimated {
		return "约 "
	}
	return ""
}

// OtherLabel names the other side of a two-currency bill in the shortcut's
// edit menu and in messages that point to it.
func OtherLabel(k recognize.Kind) string {
	if k.IsTransfer() {
		return "转入金额"
	}
	return "入账金额"
}
