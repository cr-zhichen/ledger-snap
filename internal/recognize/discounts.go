package recognize

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

// The decision model picks one amount per question, so it cannot add up a
// discount split over several rows, such as 满减 and 京豆抵扣 with no total.
// When a screenshot may have such rows, the vision model reads the total in
// parallel with the decision request; it is used only when the decision
// model picked one of several discount rows that do not settle it.

// discountWord marks a row that may hold a discount.
var discountWord = regexp.MustCompile(`优惠|立减|满减|减免|抵扣|折扣|[券劵]|红包|返现|立省|补贴`)

// discountRows lists the amounts that may be discounts: negative ones and
// ones on a row that names a discount.
func discountRows(cands []AmountCandidate) []AmountCandidate {
	var out []AmountCandidate
	for _, c := range cands {
		if c.Negative || discountWord.MatchString(c.Context) {
			out = append(out, c)
		}
	}
	return out
}

// mayAddUpDiscounts reports two or more discount rows besides the largest
// amount, which is usually what was paid, even when shown as negative.
func mayAddUpDiscounts(cands []AmountCandidate) bool {
	if len(cands) < 3 {
		return false
	}
	rows := map[string]bool{}
	for _, c := range discountRows(cands[1:]) {
		rows[c.Context] = true
	}
	return len(rows) >= 2
}

// DiscountCheck is the vision model's reading of a discount total.
type DiscountCheck struct {
	Items []string `json:"items,omitempty"`
	Total string   `json:"total,omitempty"`
	Ms    int64    `json:"ms,omitempty"`
	Error string   `json:"error,omitempty"`
	// Note says whether the total was used, and why not.
	Note string `json:"note"`
}

// discountCheck is a vision request running beside the decision request.
type discountCheck struct {
	cancel context.CancelFunc
	done   chan struct{}
	res    DiscountCheck
	cost   float64
}

const discountInstructions = `你是记账助手。截图里这笔交易可能同时享受了几项优惠，例如满减、店铺优惠、优惠券、红包或积分抵扣、立减。找出这笔交易本身用掉的每一项优惠，算出一共优惠了多少钱。
- 页面有优惠合计（例如“共优惠”“已优惠”“总优惠”）时直接取合计，不要再和各项相加。
- 广告、推荐商品、付款后领到的券、以后才能用的优惠都不算；运费、包装费等额外费用也不是优惠。
- items 逐项写出用掉的优惠，例如“满减 30.00”；没有优惠时 items 为空、discount 为空字符串。
- 只输出 JSON。`

var discountSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"items":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "这笔交易用掉的每一项优惠和金额"},
		"discount": map[string]any{"type": "string", "description": "优惠合计，两位小数，不带符号；没有优惠则为空字符串"},
	},
	"required":             []string{"items", "discount"},
	"additionalProperties": false,
}

// startDiscountCheck asks the vision model for the discount total. The
// result waits in the check until wait is called; cancel drops it.
func (r *Recognizer) startDiscountCheck(ctx context.Context, rows []Row, image []byte) *discountCheck {
	cfg := r.Config.Fallback
	ctx, cancel := context.WithCancel(ctx)
	c := &discountCheck{cancel: cancel, done: make(chan struct{})}
	req := fallbackModel(cfg).request("discount", discountSchema, discountInstructions+"\n\nOCR 文字（从上到下）：\n"+rowsText(rows), image)
	go func() {
		defer close(c.done)
		start := time.Now()
		var out struct {
			Items    []string `json:"items"`
			Discount string   `json:"discount"`
		}
		resp, err := r.Router.ChatJSON(ctx, req, &out)
		c.res.Ms = time.Since(start).Milliseconds()
		if resp != nil {
			c.cost = resp.Usage.Cost
		}
		if err != nil {
			c.res.Error = err.Error()
			return
		}
		c.res.Items = out.Items
		if v, err := amountIn(out.Discount); err == nil {
			c.res.Total = v
		}
	}()
	return c
}

// discountTotal returns the vision model's total when the decision model
// picked one of several discount rows that do not settle the total by
// themselves. The total counts only when it is one of the discount rows,
// such as a total row, or some of them added up.
func discountTotal(check *discountCheck, cands []AmountCandidate, paid, picked int64, trace *Trace) (int64, bool) {
	if check == nil || picked == 0 {
		return 0, false
	}
	var values, others []int64
	for _, c := range discountRows(cands) {
		v := toCents(c.Value)
		if v == paid {
			continue
		}
		values = append(values, v)
		if v != picked {
			others = append(others, v)
		}
	}
	// The picked row is the only discount, or the total of the others.
	if len(others) == 0 || sum(others) == picked {
		return 0, false
	}
	<-check.done
	res := check.res
	total := toCents(res.Total)
	used := false
	switch {
	case res.Error != "":
		res.Note = "视觉模型出错，用决策模型选的那一项"
	case total == 0:
		res.Note = "视觉模型没找到优惠，用决策模型选的那一项"
	case !addsUp(total, values):
		res.Note = fmt.Sprintf("视觉模型的合计 %s 和截图里的优惠行对不上，用决策模型选的那一项", res.Total)
	default:
		res.Note, used = "采用视觉模型的合计", true
	}
	trace.DiscountCheck, trace.Cost = &res, trace.Cost+check.cost
	if !used {
		return 0, false
	}
	return total, true
}

// addsUp reports whether total is one of the values or a sum of some of them.
func addsUp(total int64, values []int64) bool {
	values = values[:min(len(values), 12)]
	for set := 1; set < 1<<len(values); set++ {
		var s int64
		for i, v := range values {
			if set&(1<<i) != 0 {
				s += v
			}
		}
		if s == total {
			return true
		}
	}
	return false
}

func sum(values []int64) int64 {
	var s int64
	for _, v := range values {
		s += v
	}
	return s
}
