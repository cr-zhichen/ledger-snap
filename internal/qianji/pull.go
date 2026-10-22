package qianji

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Cursor is a position in a /syncv2/pull run. A run starts at book NoID with
// the LastTimes the previous run ended with (none for a full sync), then
// follows the book, offset and sign each page returns, keeping LastTimes.
type Cursor struct {
	BookID     string
	PageOffset int64
	PageSign   string
	LastTimes  json.RawMessage
}

// RemoteBill is a bill as /syncv2/pull returns it.
type RemoteBill struct {
	ID   string
	Time int64 // Unix seconds
	Raw  json.RawMessage
}

// PullPage is one page of the bills changed since the cursor's LastTimes,
// not a snapshot: Deletes lists the bills removed since then.
type PullPage struct {
	Changes []RemoteBill
	Deletes []string
	HasMore bool
	Next    Cursor // where the next page is, when HasMore
	// LastTimes is where the next run starts; only the last page's counts.
	LastTimes json.RawMessage
}

// PullBills reads one page of bill changes across all books.
func (c *Client) PullBills(ctx context.Context, s Session, cur Cursor) (PullPage, error) {
	form := url.Values{"bookid": {cur.BookID}, "pageoffset": {strconv.FormatInt(cur.PageOffset, 10)}, "pagesign": {cur.PageSign}}
	if len(cur.LastTimes) > 0 {
		form.Set("lasttimes", string(cur.LastTimes))
	}
	data, err := c.post(ctx, "/syncv2/pull", s, form)
	if err != nil {
		return PullPage{}, err
	}
	invalid := func(msg string) (PullPage, error) {
		return PullPage{}, &Error{Kind: ErrInvalidResponse, Path: "/syncv2/pull", Message: msg}
	}
	var raw struct {
		Changes    []json.RawMessage `json:"changes"`
		Deletes    []json.RawMessage `json:"deletes"`
		BookID     json.RawMessage   `json:"bookid"`
		PageOffset *int64            `json:"pageoffset"`
		PageSign   *string           `json:"pagesign"`
		HasMore    *int              `json:"hasmore"`
		LastTimes  json.RawMessage   `json:"lasttimes"`
	}
	if json.Unmarshal(data, &raw) != nil || raw.Changes == nil || raw.Deletes == nil ||
		raw.PageOffset == nil || raw.PageSign == nil || raw.HasMore == nil || (*raw.HasMore != 0 && *raw.HasMore != 1) {
		return invalid("响应缺少 changes、deletes 或分页字段")
	}
	book, ok := parseID(raw.BookID, true)
	if !ok || *raw.PageOffset < 0 {
		return invalid("分页字段无效")
	}
	page := PullPage{
		HasMore:   *raw.HasMore == 1,
		Next:      Cursor{BookID: book, PageOffset: *raw.PageOffset, PageSign: *raw.PageSign, LastTimes: cur.LastTimes},
		LastTimes: raw.LastTimes,
	}
	if page.HasMore && (page.Next.PageSign == "" || page.Next.BookID == cur.BookID && page.Next.PageOffset == cur.PageOffset && page.Next.PageSign == cur.PageSign) {
		return invalid("下一页的位置没有变化")
	}
	for _, b := range raw.Changes {
		var bill struct {
			ID   json.RawMessage `json:"id"`
			Time json.Number     `json:"time"`
		}
		if json.Unmarshal(b, &bill) != nil {
			return invalid("账单不是 JSON 对象")
		}
		id, ok := parseID(bill.ID, false)
		t, err := bill.Time.Int64()
		if !ok || err != nil || t < 0 {
			return invalid("账单 ID 或时间无效")
		}
		page.Changes = append(page.Changes, RemoteBill{ID: id, Time: t, Raw: b})
	}
	for _, d := range raw.Deletes {
		id, ok := parseID(d, false)
		if !ok {
			return invalid("删除的账单 ID 无效")
		}
		page.Deletes = append(page.Deletes, id)
	}
	return page, nil
}

// parseID reads a positive 64-bit ID sent as a JSON number or string, or NoID
// when allowed.
func parseID(raw json.RawMessage, allowNoID bool) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, `"`) && json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	if allowNoID && s == NoID {
		return s, true
	}
	if s == "" || s[0] < '1' || s[0] > '9' {
		return "", false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return s, err == nil && n > 0
}
