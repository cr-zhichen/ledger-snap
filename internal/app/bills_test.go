package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"ledger-snap/internal/recognize"
	"ledger-snap/internal/store"
)

func TestReconcile(t *testing.T) {
	ours := `{"id":1,"type":0,"money":25.00,"time":1767225600,"remark":"麦当劳","cateid":101,"assetid":201,"fromid":-1,"targetid":-1}`
	d := &recognize.Draft{Status: "ok", Kind: recognize.KindExpense, Money: "25.00", Currency: "CNY", CategoryID: "101", AssetID: "201",
		Time: time.Unix(1767225600, 0), TimeSource: "screenshot", Merchant: "麦当劳", Uncertain: []string{"category", "time"}}

	t.Run("unchanged", func(t *testing.T) {
		// Qianji writes numbers its own way; that is not a change.
		got, err := reconcile(d, ours, `{"id":"1","type":"0","money":25,"time":1767225600,"remark":"麦当劳","cateid":"101","assetid":"201","images":["x"]}`)
		if err != nil {
			t.Fatal(err)
		}
		if got.Money != "25.00" || got.Currency != "CNY" || got.CategoryID != "101" || !slices.Equal(got.Uncertain, d.Uncertain) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("changed in app", func(t *testing.T) {
		got, err := reconcile(d, ours, `{"id":1,"type":0,"money":30,"time":1767229200,"remark":"麦当劳早餐","cateid":102,"assetid":202}`)
		if err != nil {
			t.Fatal(err)
		}
		if got.CategoryID != "102" || got.AssetID != "202" || got.Money != "30.00" || got.Currency != "" || got.Merchant != "麦当劳早餐" ||
			got.Time.Unix() != 1767229200 || got.TimeSource != "app" || len(got.Uncertain) != 0 {
			t.Fatalf("got %+v", got)
		}
		if d.CategoryID != "101" || len(d.Uncertain) != 2 {
			t.Fatalf("draft changed: %+v", d)
		}
	})
	t.Run("foreign amount", func(t *testing.T) {
		got, err := reconcile(d, ours, `{"id":1,"type":0,"money":93.5,"time":1767225600,"remark":"麦当劳","cateid":101,"assetid":201,
			"extra":{"curr":{"ss":"USD","sv":12.99,"ts":"CNY","tv":93.5,"bs":"CNY","bv":93.5}}}`)
		if err != nil {
			t.Fatal(err)
		}
		if got.Money != "12.99" || got.Currency != "USD" || got.Converted != "93.50" || got.ConvertedCurrency != "CNY" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("turned into a transfer", func(t *testing.T) {
		got, err := reconcile(d, ours, `{"id":1,"type":2,"money":25,"time":1767225600,"remark":"麦当劳","cateid":-1,"assetid":-1,"fromid":201,"targetid":202}`)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != recognize.KindTransfer || got.AssetID != "201" || got.TargetID != "202" || got.CategoryID != "" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("unsupported type", func(t *testing.T) {
		if _, err := reconcile(d, ours, `{"id":1,"type":5,"money":25,"time":1767225600}`); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestHistory(t *testing.T) {
	s, u, _ := newTestService(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.Local)
	day := int64(24 * 3600)
	bill := func(id string, daysAgo int64, body string) store.Bill {
		at := now.Unix() - daysAgo*day
		return store.Bill{ID: id, Time: at, Raw: fmt.Sprintf(`{"id":%s,"time":%d,%s}`, id, at, body)}
	}
	bills := []store.Bill{
		bill("1", 1, `"type":0,"money":28,"remark":"美团","cateid":101,"assetid":201`),
		bill("2", 2, `"type":1,"money":5,"remark":"美团","cateid":103,"assetid":201`),
		bill("3", 3, `"type":2,"money":50,"remark":"美团","fromid":201,"targetid":202`),
		bill("4", 400, `"type":0,"money":9,"remark":"美团","cateid":102,"assetid":201`),
		bill("5", 1, `"type":0,"money":9,"remark":"美团外卖","cateid":102,"assetid":201`),
		bill("6", 4, `"type":0,"money":12.99,"remark":"美团","cateid":102,"assetid":201,"extra":{"curr":{"ss":"USD","sv":12.99,"ts":"CNY","tv":93.5}}`),
	}
	st, _ := s.Store.SyncState(ctx, u.UID)
	if err := s.Store.ApplySync(ctx, u.UID, st, `{}`, bills, nil, true, 1); err != nil {
		t.Fatal(err)
	}
	got, err := s.history(ctx, u.UID, []string{"美团", "肯德基"}, now)
	if err != nil {
		t.Fatal(err)
	}
	// Newest first; the transfer, the bill from over a year ago and the
	// other remark are left out.
	want := []recognize.PastBill{
		{Time: time.Unix(now.Unix()-day, 0), Kind: recognize.KindExpense, Money: "28.00", CategoryID: "101", AssetID: "201"},
		{Time: time.Unix(now.Unix()-2*day, 0), Kind: recognize.KindIncome, Money: "5.00", CategoryID: "103", AssetID: "201"},
		{Time: time.Unix(now.Unix()-4*day, 0), Kind: recognize.KindExpense, Money: "12.99", Currency: "USD", CategoryID: "102", AssetID: "201"},
	}
	if len(got) != 1 || !slices.Equal(got["美团"], want) {
		t.Fatalf("got %+v", got)
	}
}

func TestMergeBill(t *testing.T) {
	// Shaped like a real pull: empty fields left out, account names and
	// display fields filled in by Qianji.
	theirs := `{"id":1767225600000100001,"userid":"u1","bookid":3001,"type":0,"money":25,"remark":"麦当劳早餐","time":1767225600,
		"createtime":1767225600,"cateid":102,"assetid":201,"status":1,"platform":0,"fromact":"招行","descinfo":"招行","bookname":null,
		"images":["a.jpg"],"extra":{"flag":3,"curr":{"ss":"CNY","sv":25}}}`
	before := map[string]any{"type": 0, "money": json.Number("25.00"), "remark": "麦当劳", "cateid": json.Number("101"), "assetid": json.Number("201"),
		"fromid": json.Number("-1"), "targetid": json.Number("-1"), "extra": map[string]any{"curr": map[string]any{"ss": "CNY", "sv": json.Number("25.00")}}}
	check := func(t *testing.T, got map[string]any, want map[string]string) {
		t.Helper()
		for k, v := range want {
			if s, ok := got[k]; v == "" && ok {
				t.Errorf("%s = %s, want it left out", k, toJSON(s))
			} else if v != "" && toJSON(s) != v {
				t.Errorf("%s = %s, want %s", k, toJSON(s), v)
			}
		}
	}

	t.Run("amount", func(t *testing.T) {
		after := map[string]any{"type": 0, "money": json.Number("40.00"), "remark": "麦当劳", "cateid": json.Number("101"), "assetid": json.Number("201"),
			"fromid": json.Number("-1"), "targetid": json.Number("-1")}
		got, err := mergeBill(theirs, before, after)
		if err != nil {
			t.Fatal(err)
		}
		// The edit changed the amount and dropped the currency; the app's
		// category, remark, book, names, images and flag stay.
		check(t, got, map[string]string{
			"id": "1767225600000100001", "bookid": "3001", "money": "40.00", "remark": `"麦当劳早餐"`, "cateid": "102", "assetid": "201",
			"fromact": `"招行"`, "descinfo": `"招行"`, "images": `["a.jpg"]`, "extra": `{"flag":3}`,
			"fromid": "-1", "targetid": "-1", "status": "2", "createtime": "1767225600", "bookname": "",
		})
	})
	t.Run("account", func(t *testing.T) {
		after := map[string]any{"type": 0, "money": json.Number("25.00"), "remark": "麦当劳", "cateid": json.Number("101"), "assetid": json.Number("202"),
			"fromid": json.Number("-1"), "targetid": json.Number("-1"), "extra": map[string]any{"curr": map[string]any{"ss": "CNY", "sv": json.Number("25.00")}}}
		got, err := mergeBill(theirs, before, after)
		if err != nil {
			t.Fatal(err)
		}
		// The old account's name goes with it.
		check(t, got, map[string]string{"assetid": "202", "cateid": "102", "money": "25", "fromact": "", "descinfo": "", "extra": `{"curr":{"ss":"CNY","sv":25},"flag":3}`})
	})
}
