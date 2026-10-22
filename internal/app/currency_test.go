package app

import (
	"reflect"
	"testing"

	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
)

func TestMoneyOf(t *testing.T) {
	c := qianji.Catalog{
		Assets: []qianji.Asset{
			{ID: "cny", Name: "工行卡", Currency: "CNY"},
			{ID: "cny2", Name: "招行卡", Currency: "CNY"},
			{ID: "hkd", Name: "汇丰港元", Currency: "HKD"},
			{ID: "usd", Name: "汇丰美元", Currency: "USD"},
		},
		Prices: map[string]float64{"CNY": 1, "HKD": 0.852588, "USD": 6.69344},
	}
	cases := []struct {
		name  string
		draft recognize.Draft
		want  billMoney
	}{
		{
			name:  "base currency only",
			draft: recognize.Draft{Kind: recognize.KindExpense, Money: "25.00", Currency: "CNY", AssetID: "cny"},
			want:  billMoney{Value: "25.00", Text: "25.00"},
		},
		{
			name:  "base currency transfer",
			draft: recognize.Draft{Kind: recognize.KindTransfer, Money: "100.00", AssetID: "cny", TargetID: "cny2"},
			want:  billMoney{Value: "100.00", Text: "100.00"},
		},
		{
			name:  "exchange with both amounts shown",
			draft: recognize.Draft{Kind: recognize.KindTransfer, Money: "3940.45", Currency: "HKD", Converted: "500.00", ConvertedCurrency: "USD", AssetID: "hkd", TargetID: "usd"},
			want: billMoney{
				Value:    "3940.45",
				Currency: &qianji.Currency{Source: "HKD", SourceValue: "3940.45", Target: "USD", TargetValue: "500.00", Base: "CNY", BaseValue: "3359.58"},
				Other:    "500.00", OtherCurrency: "USD",
				Text: "3940.45 HKD → 500.00 USD",
			},
		},
		{
			name:  "exchange read the other way round",
			draft: recognize.Draft{Kind: recognize.KindTransfer, Money: "500.00", Currency: "USD", Converted: "3940.45", ConvertedCurrency: "HKD", AssetID: "hkd", TargetID: "usd"},
			want: billMoney{
				Value:    "3940.45",
				Currency: &qianji.Currency{Source: "HKD", SourceValue: "3940.45", Target: "USD", TargetValue: "500.00", Base: "CNY", BaseValue: "3359.58"},
				Other:    "500.00", OtherCurrency: "USD",
				Text: "3940.45 HKD → 500.00 USD",
			},
		},
		{
			name:  "exchange without the bought amount",
			draft: recognize.Draft{Kind: recognize.KindTransfer, Money: "3940.45", AssetID: "hkd", TargetID: "usd"},
			want: billMoney{
				Value:     "3940.45",
				Currency:  &qianji.Currency{Source: "HKD", SourceValue: "3940.45", Target: "USD", TargetValue: "501.92", Base: "CNY", BaseValue: "3359.58"},
				Estimated: true,
				Other:     "501.92", OtherCurrency: "USD",
				Text: "3940.45 HKD → 约 501.92 USD",
			},
		},
		{
			name:  "exchange before the accounts are picked",
			draft: recognize.Draft{Kind: recognize.KindTransfer, Money: "7881.80", Currency: "HKD", Converted: "1000.00", ConvertedCurrency: "USD"},
			want: billMoney{
				Value:    "7881.80",
				Currency: &qianji.Currency{Source: "HKD", SourceValue: "7881.80", Target: "USD", TargetValue: "1000.00", Base: "CNY", BaseValue: "6719.93"},
				Other:    "1000.00", OtherCurrency: "USD",
				Text: "7881.80 HKD → 1000.00 USD",
			},
		},
		{
			name:  "base currency into a foreign account",
			draft: recognize.Draft{Kind: recognize.KindTransfer, Money: "1000.00", Currency: "CNY", Converted: "1172.90", AssetID: "cny", TargetID: "hkd"},
			want: billMoney{
				Value:    "1000.00",
				Currency: &qianji.Currency{Source: "CNY", SourceValue: "1000.00", Target: "HKD", TargetValue: "1172.90"},
				Other:    "1172.90", OtherCurrency: "HKD",
				Text: "1000.00 CNY → 1172.90 HKD",
			},
		},
		{
			name:  "foreign purchase with the card's amount",
			draft: recognize.Draft{Kind: recognize.KindExpense, Money: "12.99", Currency: "USD", Converted: "93.50", ConvertedCurrency: "CNY", AssetID: "cny"},
			want: billMoney{
				Value:    "93.50",
				Currency: &qianji.Currency{Source: "USD", SourceValue: "12.99", Target: "CNY", TargetValue: "93.50", Base: "CNY", BaseValue: "93.50"},
				Other:    "93.50", OtherCurrency: "CNY",
				Text: "12.99 USD = 93.50 CNY",
			},
		},
		{
			name:  "foreign purchase read the other way round",
			draft: recognize.Draft{Kind: recognize.KindExpense, Money: "93.50", Currency: "CNY", Converted: "12.99", ConvertedCurrency: "USD", AssetID: "cny"},
			want: billMoney{
				Value:    "93.50",
				Currency: &qianji.Currency{Source: "USD", SourceValue: "12.99", Target: "CNY", TargetValue: "93.50", Base: "CNY", BaseValue: "93.50"},
				Other:    "93.50", OtherCurrency: "CNY",
				Text: "12.99 USD = 93.50 CNY",
			},
		},
		{
			name:  "foreign purchase without the card's amount",
			draft: recognize.Draft{Kind: recognize.KindExpense, Money: "12.99", Currency: "USD", AssetID: "cny"},
			want: billMoney{
				Value:     "86.95",
				Currency:  &qianji.Currency{Source: "USD", SourceValue: "12.99", Target: "CNY", TargetValue: "86.95", Base: "CNY", BaseValue: "86.95"},
				Estimated: true,
				Other:     "86.95", OtherCurrency: "CNY",
				Text: "12.99 USD = 约 86.95 CNY",
			},
		},
		{
			name:  "foreign account in its own currency",
			draft: recognize.Draft{Kind: recognize.KindExpense, Money: "45.00", Currency: "HKD", AssetID: "hkd"},
			want: billMoney{
				Value:    "45.00",
				Currency: &qianji.Currency{Source: "HKD", SourceValue: "45.00", Base: "CNY", BaseValue: "38.37"},
				Text:     "45.00 HKD",
			},
		},
		{
			name:  "unlabeled amount takes the account's currency",
			draft: recognize.Draft{Kind: recognize.KindIncome, Money: "45.00", AssetID: "hkd"},
			want: billMoney{
				Value:    "45.00",
				Currency: &qianji.Currency{Source: "HKD", SourceValue: "45.00", Base: "CNY", BaseValue: "38.37"},
				Text:     "45.00 HKD",
			},
		},
		{
			name:  "expense discount is only noted",
			draft: recognize.Draft{Kind: recognize.KindExpense, Money: "41.20", Discount: "5.00", AssetID: "cny"},
			want:  billMoney{Value: "41.20", Transfee: "-5.00", Text: "41.20（优惠 5.00）"},
		},
		{
			name:  "repayment discount reaches the card",
			draft: recognize.Draft{Kind: recognize.KindCredit, Money: "995.00", Discount: "5.00", AssetID: "cny", TargetID: "cny2"},
			want:  billMoney{Value: "995.00", Transfee: "-5.00", Text: "995.00（优惠 5.00，到账 1000.00）"},
		},
		{
			name:  "withdrawal fee leaves the source on top",
			draft: recognize.Draft{Kind: recognize.KindTransfer, Money: "1001.00", Fee: "1.00", AssetID: "cny", TargetID: "cny2"},
			want:  billMoney{Value: "1000.00", Transfee: "1.00", Text: "1001.00（手续费 1.00，到账 1000.00）"},
		},
		{
			name:  "income has no discount",
			draft: recognize.Draft{Kind: recognize.KindIncome, Money: "100.00", Discount: "5.00", Fee: "1.00", AssetID: "cny"},
			want:  billMoney{Value: "100.00", Text: "100.00"},
		},
		{
			name:  "two currencies carry the fee in both amounts",
			draft: recognize.Draft{Kind: recognize.KindTransfer, Money: "1000.00", Currency: "CNY", Converted: "1172.90", Fee: "1.00", AssetID: "cny", TargetID: "hkd"},
			want: billMoney{
				Value:    "1000.00",
				Currency: &qianji.Currency{Source: "CNY", SourceValue: "1000.00", Target: "HKD", TargetValue: "1172.90"},
				Other:    "1172.90", OtherCurrency: "HKD",
				Text: "1000.00 CNY → 1172.90 HKD",
			},
		},
		{
			name:  "foreign currency without an account",
			draft: recognize.Draft{Kind: recognize.KindExpense, Money: "10.00", Currency: "USD"},
			want: billMoney{
				Value:    "10.00",
				Currency: &qianji.Currency{Source: "USD", SourceValue: "10.00", Base: "CNY", BaseValue: "66.93"},
				Text:     "10.00 USD",
			},
		},
	}
	for _, tc := range cases {
		got, err := moneyOf(&tc.draft, c)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v %+v\nwant %+v %+v", tc.name, got, got.Currency, tc.want, tc.want.Currency)
		}
	}

	// A missing price cannot be estimated; the user types the amount instead.
	d := recognize.Draft{Kind: recognize.KindExpense, Money: "100.00", Currency: "THB", AssetID: "cny"}
	got, err := moneyOf(&d, c)
	if err == nil || got.OtherCurrency != "CNY" {
		t.Errorf("missing price: got %+v, err %v", got, err)
	}
	d.Converted = "20.00"
	if got, err := moneyOf(&d, c); err != nil || got.Value != "20.00" || got.Estimated {
		t.Errorf("typed amount: got %+v, err %v", got, err)
	}

	fee := recognize.Draft{Kind: recognize.KindTransfer, Money: "1.00", Fee: "1.00", AssetID: "cny", TargetID: "cny2"}
	if _, err := moneyOf(&fee, c); err == nil {
		t.Error("a fee as large as the amount should be refused")
	}
}

// TestDiscountRoundTrip writes drafts with a discount or fee as Qianji bills
// and reads them back the way the bill cache does.
func TestDiscountRoundTrip(t *testing.T) {
	c := qianji.Catalog{Assets: []qianji.Asset{
		{ID: "1", Name: "工行卡"}, {ID: "2", Name: "招行信用卡", Type: qianji.AssetTypeCredit},
	}}
	for _, d := range []recognize.Draft{
		{Kind: recognize.KindExpense, Money: "41.20", Discount: "5.00", AssetID: "1"},
		{Kind: recognize.KindCredit, Money: "995.00", Discount: "5.00", AssetID: "1", TargetID: "2"},
		{Kind: recognize.KindTransfer, Money: "1001.00", Fee: "1.00", AssetID: "2", TargetID: "1"},
		{Kind: recognize.KindTransfer, Money: "100.00", AssetID: "1", TargetID: "2"},
	} {
		m, err := moneyOf(&d, c)
		if err != nil {
			t.Fatal(err)
		}
		qb, err := parseBill(toJSON(qianji.NewBill("u", "9", billInput(&d, c, m))))
		if err != nil {
			t.Fatal(err)
		}
		got, ok := qb.draft()
		if !ok || got.Money != d.Money || got.Discount != d.Discount || got.Fee != d.Fee {
			t.Errorf("%s %s: read back %+v", d.Kind, d.Money, got)
		}
	}
}
