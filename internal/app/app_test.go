package app

import (
	"testing"

	"ledger-snap/internal/config"
	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
)

func TestUnmatched(t *testing.T) {
	cases := []struct {
		name  string
		draft recognize.Draft
		want  bool
	}{
		{"matched", recognize.Draft{Status: "ok", Kind: recognize.KindExpense, AssetID: "a", CategoryID: "c"}, false},
		{"no account", recognize.Draft{Status: "ok", Kind: recognize.KindExpense, CategoryID: "c"}, true},
		{"unsure category", recognize.Draft{Status: "ok", Kind: recognize.KindIncome, AssetID: "a", CategoryID: "c", Uncertain: []string{"category"}}, true},
		{"unsure time only", recognize.Draft{Status: "ok", Kind: recognize.KindExpense, AssetID: "a", CategoryID: "c", Uncertain: []string{"time"}}, false},
		{"transfer without target", recognize.Draft{Status: "ok", Kind: recognize.KindTransfer, AssetID: "a"}, true},
		{"transfer matched", recognize.Draft{Status: "ok", Kind: recognize.KindCredit, AssetID: "a", TargetID: "b"}, false},
		{"rejected", recognize.Draft{Status: "rejected"}, false},
		{"refund", recognize.Draft{Status: "ok", Kind: recognize.KindRefund}, false},
	}
	for _, tc := range cases {
		if got := unmatched(&tc.draft); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestPatchDiscountAndFee(t *testing.T) {
	s := &Service{Config: &config.Config{}}
	c := qianji.Catalog{Assets: []qianji.Asset{
		{ID: "1", Name: "工行卡", Currency: "CNY"}, {ID: "2", Name: "招行信用卡", Type: qianji.AssetTypeCredit, Currency: "CNY"},
		{ID: "3", Name: "汇丰港元", Currency: "HKD"},
	}}
	repay := &recognize.Draft{Kind: recognize.KindCredit, Money: "995.00", Discount: "5.00", AssetID: "1", TargetID: "2"}
	got, err := s.applyPatch(repay, Patch{Discount: "0", Fee: "2"}, c)
	if err != nil || got.Discount != "" || got.Fee != "2.00" || repay.Discount != "5.00" {
		t.Errorf("repayment: got %+v, err %v", got, err)
	}
	expense := &recognize.Draft{Kind: recognize.KindExpense, Money: "25.00", AssetID: "1"}
	if got, err := s.applyPatch(expense, Patch{Discount: "3"}, c); err != nil || got.Discount != "3.00" {
		t.Errorf("expense discount: got %+v, err %v", got, err)
	}
	for name, tc := range map[string]struct {
		draft recognize.Draft
		patch Patch
	}{
		"expense fee":     {recognize.Draft{Kind: recognize.KindExpense, Money: "25.00", AssetID: "1"}, Patch{Fee: "1"}},
		"income discount": {recognize.Draft{Kind: recognize.KindIncome, Money: "25.00", AssetID: "1"}, Patch{Discount: "1"}},
		"negative":        {*repay, Patch{Fee: "-1"}},
		"two currencies":  {recognize.Draft{Kind: recognize.KindTransfer, Money: "1000.00", Currency: "CNY", Converted: "1172.90", AssetID: "1", TargetID: "3"}, Patch{Fee: "1"}},
	} {
		if _, err := s.applyPatch(&tc.draft, tc.patch, c); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
