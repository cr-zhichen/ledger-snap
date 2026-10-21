package recognize

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledger-snap/internal/ocr"
)

// Row is a set of OCR lines on the same visual row, joined left to right.
type Row struct {
	Text  string
	Lines []ocr.Line
	Y, H  int
}

// Rows groups lines whose vertical extents overlap. Lines must be sorted.
func Rows(lines []ocr.Line) []Row {
	var rows []Row
	for _, l := range lines {
		if n := len(rows); n > 0 && overlaps(rows[n-1], l) {
			r := &rows[n-1]
			r.Lines = append(r.Lines, l)
			continue
		}
		rows = append(rows, Row{Lines: []ocr.Line{l}, Y: l.Y, H: l.H})
	}
	for i := range rows {
		sort.Slice(rows[i].Lines, func(a, b int) bool { return rows[i].Lines[a].X < rows[i].Lines[b].X })
		parts := make([]string, len(rows[i].Lines))
		for j, l := range rows[i].Lines {
			rows[i].Lines[j].Text = halfWidth(l.Text)
			parts[j] = strings.TrimSpace(rows[i].Lines[j].Text)
		}
		rows[i].Text = strings.Join(parts, " ")
	}
	return rows
}

// halfWidth maps full-width ASCII forms (OCR often returns "：" or "（") to
// their ASCII counterparts.
func halfWidth(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		case r == 0x3000:
			return ' '
		}
		return r
	}, s)
}

func overlaps(r Row, l ocr.Line) bool {
	last := r.Lines[len(r.Lines)-1]
	top, bottom := max(last.Y, l.Y), min(last.Y+last.H, l.Y+l.H)
	return bottom-top > min(last.H, l.H)/2
}

type AmountCandidate struct {
	Key      string `json:"key"`
	Value    string `json:"value"` // two decimals, no sign
	Negative bool   `json:"negative"`
	Positive bool   `json:"positive"` // explicit "+"
	Context  string `json:"context"`  // the whole row
	Height   int    `json:"height"`
	// Currency is the code written next to the amount or named in its row,
	// Dollar for a bare "$", or "" when none is shown.
	Currency string `json:"currency,omitempty"`
}

// Dashes include the en and em dashes OCR often returns for a minus sign.
const dashes = `\-−–—`

var (
	amountRe = regexp.MustCompile(`([+` + dashes + `])?\s*([¥￥])?\s*(\d{1,3}(?:,\d{3})+|\d+)(\.\d{1,2})?\s*(元)?`)
	// Spans that must not be read as amounts: dates, clock times, percentages,
	// and exchange rates ("1 USD = 7.8809 HKD", numbers with 3+ decimals).
	notAmountRe = regexp.MustCompile(`\d{4}[` + dashes + `/.年]\d{1,2}[` + dashes + `/.月]\d{1,2}日?|\d{1,2}[` + dashes + `/月]\d{1,2}日?\s+\d{1,2}:\d{2}|\d{1,2}:\d{2}(?::\d{2})?|\d+(?:\.\d+)?\s*%|\d+(?:\.\d+)?\s*[A-Z]{3}\s*[=＝]|\d+\.\d{3,}`)
	amountLabel = regexp.MustCompile(`金额|实付|合计|付款|支付|收款|到账|消费|总计|应付|已付`)
)

// Amounts lists money-like numbers, largest text first.
func Amounts(rows []Row, limit int) []AmountCandidate {
	var out []AmountCandidate
	seen := map[string]bool{}
	for _, row := range rows {
		for _, line := range row.Lines {
			text := line.Text
			blocked := notAmountRe.FindAllStringIndex(text, -1)
			for _, m := range amountRe.FindAllStringSubmatchIndex(text, -1) {
				if inside(blocked, m[0], m[1]) {
					continue
				}
				sign, whole, frac := part(text, m, 1), part(text, m, 3), part(text, m, 4)
				digits := strings.ReplaceAll(whole, ",", "")
				if len(digits) > 9 {
					continue // order numbers, card numbers
				}
				numEnd := m[7]
				if m[8] >= 0 {
					numEnd = m[9]
				}
				currency, marked := amountCurrency(text[:m[6]], text[numEnd:], row.Text)
				if frac == "" && !marked && !amountLabel.MatchString(row.Text) {
					continue
				}
				value, err := normalizeMoney(digits + frac)
				if err != nil || value == "0.00" {
					continue
				}
				key := value + "|" + row.Text
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, AmountCandidate{
					Value: value, Negative: sign != "" && sign != "+", Positive: sign == "+",
					Context: row.Text, Height: line.H, Currency: currency,
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Height > out[j].Height })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		out[i].Key = fmt.Sprintf("a%d", i)
	}
	return out
}

func part(s string, m []int, group int) string {
	if m[2*group] < 0 {
		return ""
	}
	return s[m[2*group]:m[2*group+1]]
}

func inside(spans [][]int, start, end int) bool {
	for _, s := range spans {
		if start < s[1] && end > s[0] {
			return true
		}
	}
	return false
}

func normalizeMoney(s string) (string, error) {
	whole, frac, _ := strings.Cut(s, ".")
	if _, err := strconv.ParseUint(whole, 10, 64); err != nil {
		return "", err
	}
	frac = (frac + "00")[:2]
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	return whole + "." + frac, nil
}

type TimeCandidate struct {
	Key     string    `json:"key"`
	Time    time.Time `json:"time"`
	HasYear bool      `json:"has_year"`
	HasTime bool      `json:"has_time"` // false when only a date is shown
	Text    string    `json:"text"`
	Context string    `json:"context"`
}

var timePatterns = []struct {
	re                     *regexp.Regexp
	year, month, day, h, m int // submatch indexes, 0 = absent
	sec                    int
	relative               int // submatch with 今天/昨天
}{
	{re: regexp.MustCompile(`(20\d{2})\s*[` + dashes + `/.年]\s*(\d{1,2})\s*[` + dashes + `/.月]\s*(\d{1,2})\s*日?\s*(\d{1,2}):(\d{2})(?::(\d{2}))?`), year: 1, month: 2, day: 3, h: 4, m: 5, sec: 6},
	{re: regexp.MustCompile(`(\d{1,2})月(\d{1,2})日\s*(\d{1,2}):(\d{2})(?::(\d{2}))?`), month: 1, day: 2, h: 3, m: 4, sec: 5},
	{re: regexp.MustCompile(`(?:^|[^\d])(\d{1,2})[` + dashes + `/](\d{1,2})\s+(\d{1,2}):(\d{2})(?::(\d{2}))?`), month: 1, day: 2, h: 3, m: 4, sec: 5},
	{re: regexp.MustCompile(`(今天|昨天)\s*(\d{1,2}):(\d{2})`), relative: 1, h: 2, m: 3},
	{re: regexp.MustCompile(`(20\d{2})\s*[` + dashes + `/.年]\s*(\d{1,2})\s*[` + dashes + `/.月]\s*(\d{1,2})\s*日?`), year: 1, month: 2, day: 3},
}

// Times lists date-times shown in the screenshot, resolved in loc. Dates
// without a year take the upload year, or the previous year if that would put
// them in the future.
func Times(rows []Row, now time.Time, loc *time.Location, limit int) []TimeCandidate {
	now = now.In(loc)
	var out []TimeCandidate
	seen := map[int64]bool{}
	for _, row := range rows {
		taken := [][]int{}
		for _, p := range timePatterns {
			for _, m := range p.re.FindAllStringSubmatchIndex(row.Text, -1) {
				if inside(taken, m[0], m[1]) {
					continue
				}
				taken = append(taken, []int{m[0], m[1]})
				num := func(i int) int {
					if i == 0 || m[2*i] < 0 {
						return 0
					}
					v, _ := strconv.Atoi(row.Text[m[2*i]:m[2*i+1]])
					return v
				}
				c := TimeCandidate{Text: strings.TrimSpace(row.Text[m[0]:m[1]]), Context: row.Text, HasYear: p.year > 0 || p.relative > 0, HasTime: p.h > 0}
				year, month, day := num(p.year), num(p.month), num(p.day)
				if p.relative > 0 {
					d := now
					if part(row.Text, m, p.relative) == "昨天" {
						d = now.AddDate(0, 0, -1)
					}
					year, month, day = d.Year(), int(d.Month()), d.Day()
				}
				if year == 0 {
					year = now.Year()
				}
				hour, minute, second := num(p.h), num(p.m), num(p.sec)
				if !c.HasTime {
					hour = 12
				}
				t := time.Date(year, time.Month(month), day, hour, minute, second, 0, loc)
				if t.Month() != time.Month(month) || hour > 23 || minute > 59 {
					continue
				}
				if !c.HasYear && t.After(now.Add(24*time.Hour)) {
					t = t.AddDate(-1, 0, 0)
				}
				if seen[t.Unix()] {
					continue
				}
				seen[t.Unix()] = true
				c.Time = t
				out = append(out, c)
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		out[i].Key = fmt.Sprintf("t%d", i)
	}
	return out
}

// statusBar reports whether a row sits in the top strip of the screen.
func statusBar(r Row, imageHeight int) bool {
	return imageHeight > 0 && r.Y+r.H < imageHeight*4/100
}
