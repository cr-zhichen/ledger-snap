package app

import (
	"testing"

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
