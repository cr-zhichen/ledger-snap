package qianji

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func pullServer(t *testing.T, data string, check func(*http.Request)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/syncv2/pull" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = r.ParseForm()
		if check != nil {
			check(r)
		}
		_, _ = w.Write([]byte(`{"ec":200,"em":"","data":` + data + `}`))
	}))
	t.Cleanup(srv.Close)
	return New(Options{BaseURL: srv.URL, Timeout: 5 * time.Second, VersionCode: 1})
}

var testSession = Session{UID: "u1", Token: "t", Device: "d"}

func TestPullBills(t *testing.T) {
	c := pullServer(t, `{"changes":[{"id":1767225600000100001,"time":1767225600,"money":12.5},{"id":"1767225600000100002","time":"1767225601"}],
		"deletes":[1767225600000100003,"1767225600000100004"],"bookid":"2001","pageoffset":50,"pagesign":"abc","hasmore":1,"lasttimes":{"x":1}}`,
		func(r *http.Request) {
			if r.Header.Get("htoken") != "1" {
				t.Errorf("htoken = %q", r.Header.Get("htoken"))
			}
			if r.Form.Get("bookid") != "-1" || r.Form.Get("pageoffset") != "0" || r.Form.Get("lasttimes") != `{"a":2}` {
				t.Errorf("form = %v", r.Form)
			}
		})
	page, err := c.PullBills(context.Background(), testSession, Cursor{BookID: NoID, LastTimes: json.RawMessage(`{"a":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Changes) != 2 || page.Changes[0].ID != "1767225600000100001" || page.Changes[0].Time != 1767225600 ||
		page.Changes[1].ID != "1767225600000100002" || page.Changes[1].Time != 1767225601 {
		t.Fatalf("changes = %+v", page.Changes)
	}
	if string(page.Changes[0].Raw) != `{"id":1767225600000100001,"time":1767225600,"money":12.5}` {
		t.Fatalf("raw = %s", page.Changes[0].Raw)
	}
	if !slices.Equal(page.Deletes, []string{"1767225600000100003", "1767225600000100004"}) {
		t.Fatalf("deletes = %v", page.Deletes)
	}
	// The next page keeps the run's LastTimes, not the one this page sent.
	if !page.HasMore || page.Next.BookID != "2001" || page.Next.PageOffset != 50 || page.Next.PageSign != "abc" || string(page.Next.LastTimes) != `{"a":2}` {
		t.Fatalf("next = %+v", page.Next)
	}
	if string(page.LastTimes) != `{"x":1}` {
		t.Fatalf("lasttimes = %s", page.LastTimes)
	}
}

func TestPullBillsRejectsBadPages(t *testing.T) {
	cases := map[string]string{
		"no deletes":    `{"changes":[],"bookid":-1,"pageoffset":0,"pagesign":"","hasmore":0}`,
		"bad hasmore":   `{"changes":[],"deletes":[],"bookid":-1,"pageoffset":0,"pagesign":"","hasmore":2}`,
		"no bill id":    `{"changes":[{"time":1}],"deletes":[],"bookid":-1,"pageoffset":0,"pagesign":"","hasmore":0}`,
		"no bill time":  `{"changes":[{"id":5}],"deletes":[],"bookid":-1,"pageoffset":0,"pagesign":"","hasmore":0}`,
		"bad delete":    `{"changes":[],"deletes":["x"],"bookid":-1,"pageoffset":0,"pagesign":"","hasmore":0}`,
		"stuck cursor":  `{"changes":[],"deletes":[],"bookid":-1,"pageoffset":0,"pagesign":"","hasmore":1}`,
		"negative page": `{"changes":[],"deletes":[],"bookid":-1,"pageoffset":-1,"pagesign":"s","hasmore":0}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			c := pullServer(t, data, nil)
			_, err := c.PullBills(context.Background(), testSession, Cursor{BookID: NoID})
			if !IsKind(err, ErrInvalidResponse) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestParseID(t *testing.T) {
	cases := []struct {
		raw   string
		noID  bool
		want  string
		valid bool
	}{
		{`123`, false, "123", true},
		{`"123"`, false, "123", true},
		{`-1`, true, "-1", true},
		{`-1`, false, "", false},
		{`0`, false, "", false},
		{`"+5"`, false, "", false},
		{`012`, false, "", false},
		{`99999999999999999999`, false, "", false},
		{`null`, false, "", false},
	}
	for _, tc := range cases {
		got, ok := parseID(json.RawMessage(tc.raw), tc.noID)
		if ok != tc.valid || ok && got != tc.want {
			t.Errorf("parseID(%s, %v) = %q, %v", tc.raw, tc.noID, got, ok)
		}
	}
}
