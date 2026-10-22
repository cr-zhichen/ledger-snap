package recognize

import (
	"fmt"
	"testing"

	"ledger-snap/internal/config"
	"ledger-snap/internal/openrouter"
)

func TestPickAmount(t *testing.T) {
	// An Apple Pay sheet: the amount, the pay button and the total all show 33.20.
	cands := []AmountCandidate{
		{Key: "a0", Value: "33.20"}, {Key: "a1", Value: "33.20"}, {Key: "a2", Value: "33.20"}, {Key: "a3", Value: "5.00"},
	}
	ans := openrouter.Answer{Choice: "a0", Probabilities: map[string]float64{"a0": 0.47, "a1": 0.39, "a2": 0.13, "a3": 0, "none": 0.01}}
	if money, p := pickAmount(cands, ans); money != "33.20" || p < 0.98 {
		t.Errorf("got %s %.2f, want 33.20 0.99", money, p)
	}

	// The split rows can outweigh a single row the model picked.
	ans = openrouter.Answer{Choice: "a3", Probabilities: map[string]float64{"a0": 0.3, "a1": 0.3, "a3": 0.4}}
	if money, _ := pickAmount(cands, ans); money != "33.20" {
		t.Errorf("got %s, want 33.20", money)
	}

	if money, p := pickAmount(nil, openrouter.Answer{Choice: "none", Probabilities: map[string]float64{"none": 1}}); money != "" || p != 0 {
		t.Errorf("got %q %.2f with no candidates", money, p)
	}
}

func TestAdjust(t *testing.T) {
	cfg := &config.Config{}
	cfg.Decision.Thresholds.Amount = 0.5
	r := &Recognizer{Config: cfg}
	cands := func(values ...string) []AmountCandidate {
		var out []AmountCandidate
		for i, v := range values {
			out = append(out, AmountCandidate{Key: fmt.Sprintf("a%d", i), Value: v})
		}
		return out
	}
	// pick answers with the candidate of this value, or "none".
	pick := func(cs []AmountCandidate, value string, p float64) openrouter.Answer {
		for _, c := range cs {
			if c.Value == value {
				return choice(c.Key, p)
			}
		}
		return choice("none", p)
	}
	repay := cands("1000.00", "5.00", "995.00")
	withdraw := cands("1000.00", "1.00")
	deducted := cands("1000.00", "1.00", "999.00")
	cases := []struct {
		name          string
		kind          Kind
		cands         []AmountCandidate
		amount        string
		discount, fee string
		p             float64
		money         string
		wantDiscount  string
		wantFee       string
		uncertain     bool
	}{
		{name: "repayment, paid amount picked", kind: KindCredit, cands: repay, amount: "995.00", discount: "5.00", p: 0.9, money: "995.00", wantDiscount: "5.00"},
		{name: "repayment, full amount picked", kind: KindCredit, cands: repay, amount: "1000.00", discount: "5.00", p: 0.9, money: "995.00", wantDiscount: "5.00"},
		{name: "fee charged on top", kind: KindTransfer, cands: withdraw, amount: "1000.00", fee: "1.00", p: 0.9, money: "1001.00", wantFee: "1.00"},
		{name: "fee taken out, sent amount picked", kind: KindTransfer, cands: deducted, amount: "1000.00", fee: "1.00", p: 0.9, money: "1000.00", wantFee: "1.00"},
		{name: "fee taken out, arrival picked", kind: KindTransfer, cands: deducted, amount: "999.00", fee: "1.00", p: 0.9, money: "1000.00", wantFee: "1.00"},
		{name: "unsure transfer fee is kept for the user", kind: KindTransfer, cands: withdraw, amount: "1000.00", fee: "1.00", p: 0.4, money: "1001.00", wantFee: "1.00", uncertain: true},
		{name: "unsure expense discount is left out", kind: KindExpense, cands: repay, amount: "995.00", discount: "5.00", p: 0.4, money: "995.00"},
		{name: "expenses have no fee", kind: KindExpense, cands: withdraw, amount: "1000.00", fee: "1.00", p: 0.9, money: "1000.00"},
		{name: "income has no discount", kind: KindIncome, cands: repay, amount: "995.00", discount: "5.00", p: 0.9, money: "995.00"},
		{name: "the amount itself is no discount", kind: KindExpense, cands: repay, amount: "995.00", discount: "1000.00", p: 0.9, money: "995.00"},
	}
	for _, tc := range cases {
		d := &Draft{Kind: tc.kind, Money: tc.amount}
		r.adjust(d, map[string]openrouter.Answer{
			"discount": pick(tc.cands, tc.discount, tc.p),
			"fee":      pick(tc.cands, tc.fee, tc.p),
		}, &Trace{Amounts: tc.cands}, nil)
		if d.Money != tc.money || d.Discount != tc.wantDiscount || d.Fee != tc.wantFee || (len(d.Uncertain) > 0) != tc.uncertain {
			t.Errorf("%s: got %+v", tc.name, d)
		}
	}
}
