package recognize

import (
	"strings"
	"testing"
)

// rowsOf builds amount candidates, largest text first, from "value|row" pairs;
// a leading "-" marks a negative amount.
func rowsOf(specs ...string) []AmountCandidate {
	var out []AmountCandidate
	for _, s := range specs {
		value, row, _ := strings.Cut(s, "|")
		neg := strings.HasPrefix(value, "-")
		out = append(out, AmountCandidate{Value: strings.TrimPrefix(value, "-"), Negative: neg, Context: row})
	}
	return out
}

var jdOrder = rowsOf("297.00|¥297.00", "329.00|商品总额 ¥329.00", "-30.00|满减 -¥30.00", "-2.00|京豆抵扣 -¥2.00", "297.00|实付款 ¥297.00")

func TestMayAddUpDiscounts(t *testing.T) {
	cases := map[string]struct {
		cands []AmountCandidate
		want  bool
	}{
		"two discount rows": {jdOrder, true},
		"one coupon":        {rowsOf("41.20|实付 ¥41.20", "13.00|起步价 ¥13.00", "-5.00|优惠券 -¥5.00"), false},
		// The paid amount shown as negative is not a discount.
		"negative paid amount": {rowsOf("-156.40|-156.40", "-10.00|红包抵扣 -¥10.00", "166.40|订单金额 ¥166.40"), false},
		"ads count too":        {rowsOf("18.00|¥18.00", "20.00|立省¥20.00", "5.00|恭喜获得5元无门槛券"), true},
	}
	for name, tc := range cases {
		if got := mayAddUpDiscounts(tc.cands); got != tc.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
}

// doneCheck is a vision check that already answered total.
func doneCheck(total string) *discountCheck {
	c := &discountCheck{cancel: func() {}, done: make(chan struct{})}
	c.res.Total = total
	close(c.done)
	return c
}

func TestDiscountTotal(t *testing.T) {
	withTotal := append(rowsOf("-32.00|共优惠 -¥32.00"), jdOrder...)
	cases := []struct {
		name   string
		cands  []AmountCandidate
		picked string
		vision string
		want   string // "" keeps the decision model's pick
		asked  bool
	}{
		{name: "rows added up", cands: jdOrder, picked: "30.00", vision: "32.00", want: "32.00", asked: true},
		{name: "total that no rows make", cands: jdOrder, picked: "30.00", vision: "31.00", asked: true},
		{name: "vision found none", cands: jdOrder, picked: "30.00", vision: "", asked: true},
		{name: "total row picked", cands: withTotal, picked: "32.00", vision: "40.00"},
		{name: "one row picked beside a total row", cands: withTotal, picked: "30.00", vision: "32.00", want: "32.00", asked: true},
		{name: "a single discount", cands: rowsOf("41.20|实付 ¥41.20", "-5.00|优惠券 -¥5.00"), picked: "5.00", vision: "9.00"},
		{name: "no discount picked", cands: jdOrder, picked: "", vision: "32.00"},
	}
	for _, tc := range cases {
		trace := &Trace{}
		total, ok := discountTotal(doneCheck(tc.vision), tc.cands, toCents("297.00"), toCents(tc.picked), trace)
		got := ""
		if ok {
			got = fromCents(total)
		}
		if got != tc.want || (trace.DiscountCheck != nil) != tc.asked {
			t.Errorf("%s: got %q, check %+v", tc.name, got, trace.DiscountCheck)
		}
	}
	if _, ok := discountTotal(nil, jdOrder, toCents("297.00"), toCents("30.00"), &Trace{}); ok {
		t.Error("no check running should keep the pick")
	}
}
