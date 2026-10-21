package qianji

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Bill types used by this project.
const (
	TypeExpense       = 0
	TypeIncome        = 1
	TypeTransfer      = 2
	TypeCreditPayment = 3
)

// NoID is the protocol's unset value for optional ID fields.
const NoID = "-1"

// NewBillID follows the client's own scheme so IDs never collide with the app's.
func NewBillID() string {
	n, err := rand.Int(rand.Reader, big.NewInt(100000))
	if err != nil {
		panic(err)
	}
	return strconv.FormatInt(time.Now().UnixMilli()*1000000+100000+n.Int64(), 10)
}

type BillInput struct {
	Type       int
	Money      string // decimal string, e.g. "25.00"
	CategoryID string
	AssetID    string // NoID when unknown
	FromID     string // transfers only
	TargetID   string // transfers only
	BookID     string
	Time       time.Time
	Remark     string
	FromName   string // transfers only, shown by the app as "A->B"
	TargetName string
}

// NewBill builds a full bill object. Every ID field must be present: the
// server rejects bills that omit unused IDs instead of sending -1.
func NewBill(uid, id string, in BillInput) map[string]any {
	now := time.Now().Unix()
	orNoID := func(v string) json.Number {
		if v == "" {
			return json.Number(NoID)
		}
		return json.Number(v)
	}
	bill := map[string]any{
		"id": json.Number(id), "userid": uid, "bookid": orNoID(in.BookID),
		"type": in.Type, "money": json.Number(in.Money), "remark": in.Remark,
		"time": in.Time.Unix(), "createtime": now, "updatetime": now,
		"status": 2, "platform": 0, "images": []any{},
		"cateid": orNoID(in.CategoryID), "assetid": orNoID(in.AssetID),
		"fromid": orNoID(in.FromID), "targetid": orNoID(in.TargetID),
	}
	if in.Type == TypeTransfer || in.Type == TypeCreditPayment {
		bill["extra"] = map[string]any{"transfee": json.Number("0")}
		bill["fromact"], bill["targetact"] = in.FromName, in.TargetName
		bill["descinfo"] = in.FromName + "->" + in.TargetName
	}
	return bill
}

// Touch marks an existing bill as modified before resubmitting it.
func Touch(bill map[string]any) {
	bill["updatetime"] = time.Now().Unix()
	bill["status"] = 2
}

type SyncReceipt struct {
	Created   []string
	Updated   []string
	Deleted   []string
	Conflicts []string
	Failed    bool
}

// SyncBills writes changes through /bill/syncall. IDs stay json.Number so
// 64-bit values never pass through float64.
func (c *Client) SyncBills(ctx context.Context, s Session, changes []map[string]any, deletes []string) (SyncReceipt, error) {
	payload := map[string]any{}
	if len(changes) > 0 {
		payload["changelist"] = changes
	}
	if len(deletes) > 0 {
		ids := make([]json.Number, len(deletes))
		for i, id := range deletes {
			ids[i] = json.Number(id)
		}
		payload["dellist"] = ids
	}
	v, err := json.Marshal(map[string]any{"bills": payload})
	if err != nil {
		return SyncReceipt{}, err
	}
	data, err := c.post(ctx, "/bill/syncall", s, url.Values{"v": {string(v)}})
	if err != nil {
		return SyncReceipt{}, err
	}
	var result struct {
		Sync struct {
			Bill *struct {
				Created   []json.Number   `json:"new_ids"`
				Updated   []json.Number   `json:"update_ids"`
				Deleted   []json.Number   `json:"del_ids"`
				Conflicts []json.Number   `json:"conf_ids"`
				Failed    json.RawMessage `json:"has_failed"`
			} `json:"bill"`
		} `json:"sync_result"`
	}
	if err := decodeNumbers(data, &result); err != nil || result.Sync.Bill == nil {
		return SyncReceipt{}, &Error{Kind: ErrInvalidResponse, Path: "/bill/syncall", Message: "响应缺少 sync_result.bill"}
	}
	b := result.Sync.Bill
	r := SyncReceipt{Created: strs(b.Created), Updated: strs(b.Updated), Deleted: strs(b.Deleted), Conflicts: strs(b.Conflicts)}
	switch strings.TrimSpace(string(b.Failed)) {
	case "", "false", "0", "null":
	default:
		r.Failed = true
	}
	return r, nil
}

func strs(ns []json.Number) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.String()
	}
	return out
}

func decodeNumbers(data []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	return dec.Decode(v)
}

func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// Accepted reports whether the receipt confirms the given operation.
func (r SyncReceipt) Accepted(created, updated, deleted []string) bool {
	if r.Failed {
		return false
	}
	for _, id := range created {
		if !contains(r.Created, id) && !contains(r.Updated, id) {
			return false
		}
	}
	for _, id := range updated {
		if !contains(r.Updated, id) {
			return false
		}
	}
	for _, id := range deleted {
		if !contains(r.Deleted, id) {
			return false
		}
	}
	return true
}
