package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
)

// qjBill is the part of a Qianji bill this service reads, from the bill
// cache or from what it wrote itself.
type qjBill struct {
	Type     json.Number     `json:"type"`
	Money    json.Number     `json:"money"`
	Time     json.Number     `json:"time"`
	Remark   string          `json:"remark"`
	CateID   qjID            `json:"cateid"`
	AssetID  qjID            `json:"assetid"`
	FromID   qjID            `json:"fromid"`
	TargetID qjID            `json:"targetid"`
	Extra    json.RawMessage `json:"extra"`
}

// qjID is an optional ID field, sent as a number or a string; "" when unset.
type qjID string

func (id *qjID) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "null" || s == qianji.NoID || s == "0" {
		s = ""
	}
	*id = qjID(s)
	return nil
}

// qjCurr is a bill's extra.curr; see qianji.Currency.
type qjCurr struct {
	SS string      `json:"ss"`
	SV json.Number `json:"sv"`
	TS string      `json:"ts"`
	TV json.Number `json:"tv"`
	BS string      `json:"bs"`
	BV json.Number `json:"bv"`
}

func parseBill(raw string) (qjBill, error) {
	var b qjBill
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return b, fmt.Errorf("读不懂钱迹账单：%w", err)
	}
	return b, nil
}

// kind maps the bill type to a draft kind; ok is false for types this
// service does not write, such as refunds or loans.
func (b qjBill) kind() (recognize.Kind, bool) {
	for _, k := range Kinds {
		if b.Type.String() == strconv.Itoa(billType(k)) {
			return k, true
		}
	}
	return "", false
}

func (b qjBill) unix() int64 {
	t, _ := b.Time.Int64()
	return t
}

func (b qjBill) curr() *qjCurr {
	var extra struct {
		Curr *qjCurr `json:"curr"`
	}
	if json.Unmarshal(b.Extra, &extra) != nil || extra.Curr == nil || extra.Curr.SS == "" {
		return nil
	}
	return extra.Curr
}

// transfee is the bill's extra.transfee: a fee, or a discount as a negative
// number; 0 for neither.
func (b qjBill) transfee() float64 {
	var extra struct {
		Transfee json.Number `json:"transfee"`
	}
	if json.Unmarshal(b.Extra, &extra) != nil {
		return 0
	}
	v, _ := extra.Transfee.Float64()
	return v
}

// accounts returns the paying account and, for a transfer, the receiving one.
func (b qjBill) accounts() (string, string) {
	if k, _ := b.kind(); k.IsTransfer() {
		return string(b.FromID), string(b.TargetID)
	}
	return string(b.AssetID), ""
}

// sameMoney compares the amounts of two bills, ignoring how the numbers are
// written.
func sameMoney(a, b qjBill) bool {
	ca, cb := a.curr(), b.curr()
	if !sameAmount(a.Money, b.Money) || money(a.transfee()) != money(b.transfee()) || (ca == nil) != (cb == nil) {
		return false
	}
	return ca == nil || ca.SS == cb.SS && ca.TS == cb.TS && ca.BS == cb.BS &&
		sameAmount(ca.SV, cb.SV) && sameAmount(ca.TV, cb.TV) && sameAmount(ca.BV, cb.BV)
}

func sameAmount(a, b json.Number) bool {
	return cents(a) == cents(b)
}

func cents(n json.Number) string {
	if v, err := n.Float64(); err == nil {
		return money(v)
	}
	return n.String()
}

// setMoney copies the bill's amounts into a draft the way recognition fills
// them: what was paid, what it came to in the other currency, and the
// discount or fee of a bill in the base currency (see adjust).
func (b qjBill) setMoney(d *recognize.Draft) {
	d.Converted, d.ConvertedCurrency, d.Discount, d.Fee = "", "", "", ""
	c := b.curr()
	if c == nil {
		paid, _ := b.Money.Float64()
		k, _ := b.kind()
		switch t := b.transfee(); {
		case t > 0 && k.IsTransfer():
			d.Fee, paid = money(t), paid+t
		case t < 0 && (k == recognize.KindExpense || k.IsTransfer()):
			d.Discount = money(-t)
		}
		d.Money, d.Currency = money(paid), ""
		return
	}
	d.Money, d.Currency = cents(c.SV), c.SS
	if c.TS != "" && c.TS != c.SS {
		d.Converted, d.ConvertedCurrency = cents(c.TV), c.TS
	}
}

// draft reads a bill the way recognition would have filled it.
func (b qjBill) draft() (*recognize.Draft, bool) {
	k, ok := b.kind()
	if !ok {
		return nil, false
	}
	d := &recognize.Draft{Status: "ok", Kind: k, Time: time.Unix(b.unix(), 0), Merchant: b.Remark}
	d.AssetID, d.TargetID = b.accounts()
	if !k.IsTransfer() {
		d.CategoryID = string(b.CateID)
	}
	b.setMoney(d)
	return d, true
}

const (
	// historyWindow and historySize bound the earlier bills of a merchant
	// that recognition sees.
	historyWindow = 365 * 24 * time.Hour
	historySize   = 10
)

// history finds the user's recent expenses and income whose remark is the
// merchant's name, which is what this service writes as the remark. It
// reads the bill cache, so it sees categories changed in the app too.
func (s *Service) history(ctx context.Context, uid string, merchants []string, now time.Time) (map[string][]recognize.PastBill, error) {
	found, err := s.Store.BillsByRemark(ctx, uid, merchants, now.Add(-historyWindow).Unix(), historySize)
	if err != nil {
		return nil, err
	}
	out := map[string][]recognize.PastBill{}
	for name, bills := range found {
		for _, b := range bills {
			qb, err := parseBill(b.Raw)
			if err != nil {
				continue
			}
			d, ok := qb.draft()
			if !ok || d.Kind.IsTransfer() || d.CategoryID == "" {
				continue
			}
			out[name] = append(out[name], recognize.PastBill{Time: d.Time, Kind: d.Kind, Money: d.Money, Currency: d.Currency, CategoryID: d.CategoryID, AssetID: d.AssetID})
		}
	}
	return out, nil
}

// reconcile carries what was changed in the app over to a written record's
// draft: every field whose bill value differs from what this service last
// wrote. Fields changed in the app are no longer uncertain.
func reconcile(d *recognize.Draft, ours, theirs string) (*recognize.Draft, error) {
	was, err := parseBill(ours)
	if err != nil {
		return nil, err
	}
	now, err := parseBill(theirs)
	if err != nil {
		return nil, err
	}
	out := *d
	out.Uncertain = slices.Clone(d.Uncertain)
	settle := func(fields ...string) {
		out.Uncertain = slices.DeleteFunc(out.Uncertain, func(f string) bool { return slices.Contains(fields, f) })
	}
	retyped := now.Type.String() != was.Type.String()
	if retyped {
		k, ok := now.kind()
		if !ok {
			return nil, userErr(409, "这笔账在钱迹里改成了这里不支持的类型，请在钱迹里修改")
		}
		out.Kind = k
	}
	wasFrom, wasTo := was.accounts()
	nowFrom, nowTo := now.accounts()
	if retyped || nowFrom != wasFrom || nowTo != wasTo {
		out.AssetID, out.TargetID = nowFrom, nowTo
		settle("asset", "target_asset")
	}
	if retyped || now.CateID != was.CateID {
		out.CategoryID = ""
		if !out.Kind.IsTransfer() {
			out.CategoryID = string(now.CateID)
		}
		settle("category")
	}
	if now.unix() != was.unix() {
		out.Time, out.TimeSource = time.Unix(now.unix(), 0), "app"
		settle("time")
	}
	if now.Remark != was.Remark {
		out.Merchant = now.Remark
	}
	if !sameMoney(now, was) {
		now.setMoney(&out)
	}
	return &out, nil
}

// billKeys are the bill fields this service sets. The rest, such as images,
// tags or the book, stay as the app left them.
var billKeys = []string{"type", "money", "remark", "time", "cateid", "assetid", "fromid", "targetid"}

var extraKeys = []string{"curr", "transfee"}

// nameKeys hold account names, which Qianji keeps on ordinary bills too.
// They follow the accounts: an edit that changes an account replaces them.
var nameKeys = []string{"fromact", "targetact", "descinfo"}

// derivedKeys are display fields Qianji fills in itself; edits leave them
// out, as LedgerLens does.
var derivedKeys = []string{"category", "paytype", "bookname"}

// idKeys must all be present: the server rejects bills that leave one out.
var idKeys = []string{"bookid", "cateid", "assetid", "fromid", "targetid"}

// mergeBill applies an edit to a bill as it is in Qianji: the fields in which
// the bill built from the edited draft differs from the one built from the
// draft before the edit.
func mergeBill(theirs string, before, after map[string]any) (map[string]any, error) {
	out, err := decodeObject(theirs)
	if err != nil {
		return nil, err
	}
	for _, k := range billKeys {
		overlay(out, before, after, k)
	}
	if slices.ContainsFunc([]string{"type", "assetid", "fromid", "targetid"}, func(k string) bool { return changed(before, after, k) }) {
		for _, k := range nameKeys {
			if v, ok := after[k]; ok {
				out[k] = v
			} else {
				delete(out, k)
			}
		}
	}
	for _, k := range derivedKeys {
		delete(out, k)
	}
	bx, _ := before["extra"].(map[string]any)
	ax, _ := after["extra"].(map[string]any)
	if slices.ContainsFunc(extraKeys, func(k string) bool { return changed(bx, ax, k) }) {
		extra, ok := out["extra"].(map[string]any)
		if !ok {
			extra = map[string]any{}
		}
		for _, k := range extraKeys {
			overlay(extra, bx, ax, k)
		}
		out["extra"] = extra
	}
	for _, k := range idKeys {
		if out[k] == nil {
			out[k] = json.Number(qianji.NoID)
		}
	}
	qianji.Touch(out)
	return out, nil
}

func changed(before, after map[string]any, key string) bool {
	b, hadB := before[key]
	a, hadA := after[key]
	return hadA != hadB || toJSON(a) != toJSON(b)
}

func overlay(dst, before, after map[string]any, key string) {
	if !changed(before, after, key) {
		return
	}
	if v, ok := after[key]; ok {
		dst[key] = v
	} else {
		delete(dst, key)
	}
}

// decodeObject keeps numbers as written, so 64-bit IDs survive.
func decodeObject(raw string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil || out == nil {
		return nil, fmt.Errorf("读不懂钱迹账单：%v", err)
	}
	return out, nil
}
