package server

import (
	"slices"
	"testing"

	"ledger-snap/internal/app"
)

func TestGoesBack(t *testing.T) {
	cases := []struct {
		step, value string
		want        bool
	}{
		{"asset", optBack, true},
		{"asset", "", false},
		{"money", "", true},
		{"money", "0", true},
		{"money", "0.00", true},
		{"money", "12.5", false},
		{"money", "1,200", false},
		{"other", "0", true},
		{"time", "", true},
		{"time", "0", false},
		{"text", "", true},
		{"text", "改成打车", false},
		{"main", "", false},
		{"edit", "", false},
	}
	for _, tc := range cases {
		if got := goesBack(tc.step, tc.value); got != tc.want {
			t.Errorf("goesBack(%q, %q) = %v", tc.step, tc.value, got)
		}
	}
}

func TestMainStepSaysWhetherWritten(t *testing.T) {
	cases := []struct {
		name   string
		step   *Step
		prompt string
		opts   []string
	}{
		{
			name:   "written",
			step:   mainStep(&app.Result{RecordID: "r_1", Status: "written", Summary: "支出 35.00"}, ""),
			prompt: "已记账：支出 35.00",
			opts:   []string{optDone, optEdit, optRemove},
		},
		{
			name:   "held with reason",
			step:   mainStep(&app.Result{RecordID: "r_1", Status: "needs_confirm", Summary: "支出 35.00", Reason: "有几项拿不准：分类"}, ""),
			prompt: "还没记账，有几项拿不准：分类\n支出 35.00",
			opts:   []string{optConfirm, optEdit, optDiscard},
		},
		{
			name:   "held without reason",
			step:   mainStep(&app.Result{RecordID: "r_1", Status: "needs_confirm", Summary: "支出 35.00"}, ""),
			prompt: "还没记账：支出 35.00",
			opts:   []string{optConfirm, optEdit, optDiscard},
		},
		{
			name:   "written edit",
			step:   editedStep(&app.Result{RecordID: "r_1", Status: "written", Summary: "支出 35.00"}),
			prompt: "已改好，钱迹里同步改了\n已记账：支出 35.00",
			opts:   []string{optDone, optEdit, optRemove},
		},
		{
			name:   "held edit",
			step:   editedStep(&app.Result{RecordID: "r_1", Status: "needs_confirm", Summary: "支出 35.00"}),
			prompt: "已改好\n还没记账：支出 35.00",
			opts:   []string{optConfirm, optEdit, optDiscard},
		},
		{
			name:   "written edit menu",
			step:   editStep(&app.Result{RecordID: "r_1", Status: "written", Kind: "转账", Summary: "转账 100.00"}, ""),
			prompt: "改哪一项？改完直接同步到钱迹\n已记账：转账 100.00",
			opts:   []string{optBack, optAsset, optKind, optMoney, optDiscount, optFee, optTime, optText, optRefresh},
		},
		{
			name:   "held edit menu",
			step:   editStep(&app.Result{RecordID: "r_1", Status: "needs_confirm", Kind: "转账", Summary: "转账 100.00"}, ""),
			prompt: "改哪一项？\n还没记账：转账 100.00",
			opts:   []string{optBack, optAsset, optKind, optMoney, optDiscount, optFee, optTime, optText, optRefresh},
		},
	}
	for _, tc := range cases {
		if tc.step.Prompt != tc.prompt {
			t.Errorf("%s: prompt = %q, want %q", tc.name, tc.step.Prompt, tc.prompt)
		}
		if !slices.Equal(tc.step.Options, tc.opts) {
			t.Errorf("%s: options = %q, want %q", tc.name, tc.step.Options, tc.opts)
		}
	}
}
