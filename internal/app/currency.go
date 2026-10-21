package app

import (
	"fmt"
	"math"
	"strconv"

	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
)

// billMoney is what Qianji stores for a draft's amount, worked out the way
// the app's currency dialog does (see docs/design.md 6.3).
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
	Text                 string // for the summary, e.g. "3940.45 HKD → 500.00 USD"
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
		from, ok := c.AssetCurrency(d.AssetID)
		if !ok {
			from = base
		}
		to, ok := c.AssetCurrency(d.TargetID)
		if !ok {
			to = from
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
			return m, nil
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
		}
		return m, nil
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
