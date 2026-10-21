package recognize

import (
	"testing"
	"time"

	"ledger-snap/internal/ocr"
)

func line(text string, x, y, h int) ocr.Line { return ocr.Line{Text: text, X: x, Y: y, W: 100, H: h} }

func TestAmounts(t *testing.T) {
	rows := Rows([]ocr.Line{
		line("19:02", 80, 66, 43),
		line("-36.80", 392, 498, 97),
		line("订单金额", 91, 912, 48), line("39.80", 959, 914, 43),
		line("红包优惠", 90, 1041, 48), line("–3.00", 961, 1044, 40),
		line("支付时间", 92, 1170, 48), line("2026-10-20", 643, 1173, 41), line("18:42:11", 921, 1174, 41),
		line("订单号", 92, 1686, 48), line("2026102022001488291503", 510, 1688, 42),
		line("账户余额", 92, 1800, 48), line("12,580.66", 510, 1800, 42),
		line("交易卡号 尾号5678", 92, 1900, 48),
	})
	got := Amounts(rows, 0)
	want := []struct {
		value    string
		negative bool
	}{{"36.80", true}, {"39.80", false}, {"12580.66", false}, {"3.00", true}}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Value != w.value || got[i].Negative != w.negative {
			t.Errorf("candidate %d = %+v, want %+v", i, got[i], w)
		}
	}
	if got[1].Context != "订单金额 39.80" {
		t.Errorf("context = %q", got[1].Context)
	}
}

func TestAmountsIntegerNeedsMarker(t *testing.T) {
	got := Amounts(Rows([]ocr.Line{line("¥200", 0, 0, 80), line("共 3 张", 0, 200, 30), line("128 元", 0, 300, 30)}), 0)
	if len(got) != 2 || got[0].Value != "200.00" || got[1].Value != "128.00" {
		t.Fatalf("got %+v", got)
	}
}

func TestTimes(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 10, 21, 12, 40, 0, 0, loc)
	rows := Rows([]ocr.Line{
		line("12:31", 80, 66, 43),
		line("支付时间", 92, 1170, 48), line("2026-10-20", 643, 1173, 41), line("18:42:11", 921, 1174, 41),
		line("转账时间 2026年10月21日 09:12:40", 0, 1300, 40),
		line("交易时间 10-21 08:05", 0, 1400, 40),
		line("12月31日 23:00", 0, 1500, 40),
		line("昨天 21:15", 0, 1600, 40),
		line("2026年10月19日12：31：05", 0, 1700, 40),
	})
	got := Times(rows, now, loc, 0)
	want := []string{"2026-10-20 18:42:11", "2026-10-21 09:12:40", "2026-10-21 08:05:00", "2025-12-31 23:00:00", "2026-10-20 21:15:00", "2026-10-19 12:31:05"}
	if len(got) != len(want) {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	for i, w := range want {
		if s := got[i].Time.Format("2006-01-02 15:04:05"); s != w {
			t.Errorf("time %d = %s, want %s", i, s, w)
		}
	}
}
