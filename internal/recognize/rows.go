package recognize

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"ledger-snap/internal/ocr"
)

// Rows joins lines whose boxes overlap vertically. That misses a label and
// its value whose boxes sit at different heights, a label above or below its
// value, and a value wrapped onto two lines, and an amount without its label
// is hard to tell apart from the others. So a fast text model sorts the lines
// into labels and values by their text and boxes instead. It only names line
// numbers, so it cannot change what OCR read; on any error the rows come from
// Rows.

// RowItem is one label and its value, by line number. Text without a label
// has only a value.
type RowItem struct {
	Label []int `json:"label"`
	Value []int `json:"value"`
}

const itemInstructions = `下面是手机截图经 OCR 识别出的文字行，每行开头是编号和文字框位置（x、y 是左上角，w、h 是宽和高，单位像素）。OCR 常把界面上的一项拆成几行，请把文字整理成“项”：一项是一个标签和它的值，例如标签“支付方式”、值“招商银行信用卡(1234)”。
- 标签和值可能在同一行的左右两边（两个框可能高低不齐、字号不同），也可能标签在上、值在下，或者值在上、说明它的标签在下。
- 一个值折成几行时，几行都放进 value；标签折成几行时都放进 label。一段话折成几行时，整段作为一项的 value。
- 没有标签的文字（标题、商户名、大字金额、按钮、提示）单独成一项，label 为空数组。
- 不同的项不要合并：一项最多一个金额。
- items 按从上到下的顺序列出，每个编号恰好出现在一项里。`

var itemSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"items": map[string]any{"type": "array", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"label": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"value": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			},
			"required":             []string{"label", "value"},
			"additionalProperties": false,
		}},
	},
	"required":             []string{"items"},
	"additionalProperties": false,
}

// rows groups the lines into rows: by the rows model when it is on, by box
// position otherwise or when the model fails. items reuses an earlier
// grouping of the same lines.
func (r *Recognizer) rows(ctx context.Context, lines []ocr.Line, items []RowItem, width, height int, trace *Trace) []Row {
	if items == nil && r.Config.Rows.Enabled && r.Router != nil && len(lines) > 1 {
		var err error
		if items, err = r.sortLines(ctx, lines, width, height, trace); err != nil {
			trace.RowsError = err.Error()
		}
	}
	rows := Rows(lines)
	if items != nil {
		trace.RowItems = items
		rows = itemRows(lines, items)
	}
	for _, row := range rows {
		trace.Rows = append(trace.Rows, row.Text)
	}
	return rows
}

// sortLines asks the rows model which lines are labels and values.
func (r *Recognizer) sortLines(ctx context.Context, lines []ocr.Line, width, height int, trace *Trace) ([]RowItem, error) {
	cfg := r.Config.Rows
	trace.RowsModel = cfg.Model
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n截图宽 %d、高 %d 像素。\n", itemInstructions, width, height)
	for i, l := range lines {
		fmt.Fprintf(&b, "%d [x=%d y=%d w=%d h=%d] %s\n", i, l.X, l.Y, l.W, l.H, halfWidth(l.Text))
	}
	req := rowsModel(cfg).request("items", itemSchema, b.String(), nil)
	if cfg.Timeout.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout.Duration)
		defer cancel()
	}
	var out struct {
		Items []RowItem `json:"items"`
	}
	start := time.Now()
	resp, err := r.Router.ChatJSON(ctx, req, &out)
	trace.RowsMs = time.Since(start).Milliseconds()
	if resp != nil {
		trace.Cost += resp.Usage.Cost
	}
	if err != nil {
		return nil, err
	}
	if out.Items == nil {
		return nil, fmt.Errorf("模型 %s 没有给出分项", cfg.Model)
	}
	return out.Items, nil
}

// itemRows builds a row from each item, the label first. Unknown and
// repeated line numbers are dropped and lines left out get rows of their
// own. Rows go in the order of their first line, which is top to bottom.
func itemRows(lines []ocr.Line, items []RowItem) []Row {
	owner := make([]int, len(lines)) // item number + 1; 0 for none
	kept := make([]RowItem, len(items))
	take := func(n int, nums []int) []int {
		var out []int
		for _, i := range nums {
			if i >= 0 && i < len(lines) && owner[i] == 0 {
				owner[i] = n + 1
				out = append(out, i)
			}
		}
		slices.Sort(out)
		return out
	}
	for n, it := range items {
		kept[n] = RowItem{Label: take(n, it.Label), Value: take(n, it.Value)}
	}
	var rows []Row
	done := map[int]bool{}
	for i := range lines {
		n := owner[i] - 1
		if n < 0 {
			rows = append(rows, newRow(lines, nil, []int{i}))
			continue
		}
		if !done[n] {
			done[n] = true
			it := kept[n]
			if len(it.Value) == 0 {
				// A label with nothing to label is plain text.
				it.Label, it.Value = nil, it.Label
			}
			rows = append(rows, newRow(lines, it.Label, it.Value))
		}
	}
	return rows
}

func newRow(lines []ocr.Line, label, value []int) Row {
	var row Row
	text := func(nums []int) string {
		parts := make([]string, len(nums))
		for j, i := range nums {
			l := lines[i]
			l.Text = halfWidth(l.Text)
			row.Lines = append(row.Lines, l)
			parts[j] = strings.TrimSpace(l.Text)
		}
		return strings.Join(parts, " ")
	}
	row.Label = text(label)
	row.Text = strings.TrimSpace(row.Label + " " + text(value))
	return row
}
