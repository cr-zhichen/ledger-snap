package recognize

import (
	"context"
	"fmt"
	"ledger-snap/internal/qianji"
	"maps"
	"slices"
	"strings"
)

// lookupHistory finds the earlier bills of the given merchants and notes them
// in the trace. A failed lookup only means no history.
func (r *Recognizer) lookupHistory(ctx context.Context, in Input, names []string, trace *Trace) map[string][]PastBill {
	if in.History == nil || len(names) == 0 {
		return nil
	}
	found, err := in.History(ctx, names)
	if err != nil {
		trace.HistoryError = err.Error()
		return nil
	}
	maps.DeleteFunc(found, func(_ string, past []PastBill) bool { return len(past) == 0 })
	if len(found) > 0 {
		if trace.History == nil {
			trace.History = map[string][]PastBill{}
		}
		maps.Copy(trace.History, found)
	}
	return found
}

var weekdays = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// historyText shows the earlier bills to the models, so they can follow the
// user's habits, which may depend on the time, the amount or what was bought.
func (r *Recognizer) historyText(past map[string][]PastBill, c qianji.Catalog) string {
	if len(past) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("用户以前和截图里可能的交易对方记过的账，新的在前。选分类时以用户自己的习惯为准，优先于一般常识：同一个交易对方可能因为时间段、金额或买的东西不同而记到不同分类，找出和这笔最接近的几笔，记到它们的分类。\n")
	for _, name := range slices.Sorted(maps.Keys(past)) {
		fmt.Fprintf(&b, "「%s」\n", name)
		for _, p := range past[name] {
			t := p.Time.In(r.Config.Location())
			amount := p.Money
			if p.Currency != "" {
				amount += " " + p.Currency
			}
			category := "未分类"
			if cat, ok := c.Category(p.CategoryID); ok {
				category = cat.Path
			}
			account := ""
			if a, ok := c.Asset(p.AssetID); ok {
				account = " · " + AssetLabel(a)
			}
			fmt.Fprintf(&b, "- %s %s %s · %s %s%s → %s\n", t.Format("2006-01-02"), weekdays[t.Weekday()], t.Format("15:04"), p.Kind.Label(), amount, account, category)
		}
	}
	return b.String()
}

// historyRecent is how many of a merchant's latest bills of one kind
// historyCategory looks at.
const historyRecent = 5

// historyCategory returns the category of the merchant's latest bills of the
// kind when there are at least two and they all have it. Otherwise the model
// decides, with the history in front of it: one bill may have been
// categorized wrongly and never corrected.
func historyCategory(past []PastBill, kind Kind) (string, bool) {
	var ids []string
	for _, p := range past {
		if p.Kind == kind && len(ids) < historyRecent {
			ids = append(ids, p.CategoryID)
		}
	}
	if len(ids) < 2 || ids[0] == "" {
		return "", false
	}
	for _, id := range ids[1:] {
		if id != ids[0] {
			return "", false
		}
	}
	return ids[0], true
}
