package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
)

// label is one expected transaction. Empty fields are not checked.
type label struct {
	Status   string `json:"status"` // ok
	Kind     string `json:"kind"`
	Money    string `json:"money"`
	Category string `json:"category"` // category path, e.g. "三餐"
	Asset    string `json:"asset"`    // asset name, or "-" for none
	Target   string `json:"target_asset"`
	Time     string `json:"time"`     // 2006-01-02 15:04
	Merchant string `json:"merchant"` // substring of the merchant
	Currency string `json:"currency"` // currency code of money
	// Converted is the other-currency amount as "500.00 USD".
	Converted string `json:"converted"`
	Discount  string `json:"discount"` // "-" for none
	Fee       string `json:"fee"`      // "-" for none
}

type labelFile struct {
	UploadTime string             `json:"upload_time"` // 2006-01-02 15:04
	Images     map[string][]label `json:"images"`
}

// runEval checks recognition against labels.json without writing to Qianji.
func runEval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	cfgPath := fs.String("config", "config.toml", "配置文件")
	labelsPath := fs.String("labels", "testdata/screens/labels.json", "标注文件，图片路径相对于它所在的目录")
	out := fs.String("out", "", "把每张截图的完整识别过程写到这个目录")
	repeat := fs.Int("repeat", 1, "每张截图识别几次，用来看结果是否稳定")
	only := fs.String("only", "", "只评测文件名包含这些片段的截图，逗号分隔，例如 20_,37_")
	var o overrides
	o.register(fs)
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	o.apply(cfg)
	data, err := os.ReadFile(*labelsPath)
	if err != nil {
		return err
	}
	var lf labelFile
	if err := json.Unmarshal(data, &lf); err != nil {
		return fmt.Errorf("解析标注文件: %w", err)
	}
	upload, err := time.ParseInLocation("2006-01-02 15:04", lf.UploadTime, cfg.Location())
	if err != nil {
		return fmt.Errorf("upload_time: %w", err)
	}
	rec, err := newRecognizer(cfg)
	if err != nil {
		return err
	}
	catalog, err := testCatalog(ctx, cfg)
	if err != nil {
		return err
	}
	if *out != "" {
		if err := os.MkdirAll(*out, 0o700); err != nil {
			return err
		}
	}
	var names []string
	for name := range lf.Images {
		if *only == "" || slices.ContainsFunc(strings.Split(*only, ","), func(part string) bool { return part != "" && strings.Contains(name, part) }) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	runs := max(*repeat, 1)
	fmt.Printf("提取=%s 匹配=%s", cfg.Extraction.Model, cfg.Decision.Model)
	if runs > 1 {
		fmt.Printf(" 每张 %d 次", runs)
	}
	fmt.Println()
	right, checked := map[string]int{}, map[string]int{}
	probs := map[string][]matchProb{}
	var totals []int64
	cost := 0.0
	failed := 0
	for _, name := range names {
		want := lf.Images[name]
		image, err := os.ReadFile(filepath.Join(filepath.Dir(*labelsPath), name))
		if err != nil {
			return err
		}
		passed := 0
		var problems []string
		var ms int64
		for run := range runs {
			drafts, trace, err := rec.Recognize(ctx, recognize.Input{Image: image, UploadTime: upload, Catalog: catalog})
			if *out != "" {
				base := strings.TrimSuffix(name, filepath.Ext(name))
				if runs > 1 {
					base += fmt.Sprintf("-%d", run+1)
				}
				if saveErr := writeJSON(filepath.Join(*out, base+".json"), map[string]any{"drafts": drafts, "trace": trace}); saveErr != nil {
					return saveErr
				}
			}
			cost += trace.Cost
			ms = trace.TotalMs
			var wrong []string
			if err != nil {
				wrong = []string{"失败：" + err.Error()}
			} else {
				totals = append(totals, trace.TotalMs)
				wrong = check(drafts, trace, want, catalog, cfg, right, checked, probs)
			}
			if len(wrong) == 0 {
				passed++
			} else if msg := strings.Join(wrong, "，"); !slices.Contains(problems, msg) {
				problems = append(problems, msg)
			}
		}
		mark := "✓"
		switch {
		case passed == 0:
			mark = "✗"
		case passed < runs:
			mark = "~"
		}
		if passed < runs {
			failed++
		}
		if runs == 1 {
			fmt.Printf("%s %-28s %5dms %s\n", mark, name, ms, strings.Join(problems, "；"))
		} else {
			fmt.Printf("%s %-28s %d/%d %s\n", mark, name, passed, runs, strings.Join(problems, "；"))
		}
	}
	fmt.Println("\n字段准确率：")
	for _, f := range evalFields {
		if checked[f] > 0 {
			fmt.Printf("  %-12s %d/%d\n", f, right[f], checked[f])
		}
	}
	printMatchProbs(probs, cfg)
	slices.Sort(totals)
	if n := len(totals); n > 0 {
		fmt.Printf("耗时 p50=%dms p95=%dms；总费用 $%.5f\n", totals[n/2], totals[min(n-1, n*95/100)], cost)
	}
	if failed > 0 {
		return fmt.Errorf("%d 张截图未通过评测", failed)
	}
	return nil
}

var evalFields = []string{"status", "kind", "money", "currency", "converted", "discount", "fee", "category", "asset", "target_asset", "time", "merchant"}

// matchProb is the decision model's probability for one labelled account or
// category answer, to see where the thresholds should be.
type matchProb struct {
	prob        float64
	right, held bool
}

// check compares one recognition with the labels and returns what is wrong.
func check(drafts []*recognize.Draft, trace *recognize.Trace, want []label, c qianji.Catalog, cfg *config.Config, right, checked map[string]int, probs map[string][]matchProb) []string {
	var wrong []string
	if len(drafts) != len(want) {
		wrong = append(wrong, fmt.Sprintf("笔数=%d（应为 %d）", len(drafts), len(want)))
	}
	for i := 0; i < min(len(drafts), len(want)); i++ {
		got := actual(drafts[i], c, cfg)
		for _, f := range evalFields {
			w := field(want[i], f)
			if w == "" {
				continue
			}
			checked[f]++
			ok := got[f] == w
			if f == "merchant" {
				ok = strings.Contains(got[f], w)
			}
			if ok {
				right[f]++
			} else if f == "status" && drafts[i].Reason != "" {
				wrong = append(wrong, fmt.Sprintf("第%d笔 %s", i+1, drafts[i].Reason))
			} else {
				wrong = append(wrong, fmt.Sprintf("第%d笔 %s=%q（应为 %q）", i+1, f, got[f], w))
			}
			if trace.Decision == nil {
				continue
			}
			if a, asked := trace.Decision.Answers[fmt.Sprintf("b%d_%s", i, f)]; asked {
				probs[f] = append(probs[f], matchProb{prob: a.Prob(), right: ok, held: slices.Contains(drafts[i].Uncertain, f)})
			}
		}
	}
	return wrong
}

// printMatchProbs shows how the thresholds split right and wrong answers:
// a wrong answer above the threshold is written without asking the user, a
// right one below it asks for nothing.
func printMatchProbs(probs map[string][]matchProb, cfg *config.Config) {
	if len(probs) == 0 {
		return
	}
	fmt.Println("\n匹配概率：")
	for _, f := range []string{"category", "asset", "target_asset"} {
		list := probs[f]
		if len(list) == 0 {
			continue
		}
		threshold := cfg.Decision.Thresholds.Asset
		if f == "category" {
			threshold = cfg.Decision.Thresholds.Category
		}
		var parts, wrongProbs []string
		lowest, needless, silent := 1.0, 0, 0
		for _, p := range list {
			if p.right {
				lowest = min(lowest, p.prob)
				if p.held {
					needless++
				}
			} else {
				wrongProbs = append(wrongProbs, fmt.Sprintf("%.2f", p.prob))
				if !p.held {
					silent++
				}
			}
		}
		parts = append(parts, fmt.Sprintf("正确 %d 次，最低 %.2f，其中 %d 次被多余地拦下确认", len(list)-len(wrongProbs), lowest, needless))
		if len(wrongProbs) > 0 {
			parts = append(parts, fmt.Sprintf("错误 %d 次（%s），其中 %d 次没拦下", len(wrongProbs), strings.Join(wrongProbs, " "), silent))
		}
		fmt.Printf("  %-12s 阈值 %.2f：%s\n", f, threshold, strings.Join(parts, "；"))
	}
}

func field(l label, name string) string {
	return map[string]string{"status": l.Status, "kind": l.Kind, "money": l.Money, "category": l.Category, "asset": l.Asset, "target_asset": l.Target, "time": l.Time, "merchant": l.Merchant, "currency": l.Currency, "converted": l.Converted, "discount": l.Discount, "fee": l.Fee}[name]
}

func actual(d *recognize.Draft, c qianji.Catalog, cfg *config.Config) map[string]string {
	got := map[string]string{"status": d.Status, "kind": string(d.Kind), "money": d.Money, "merchant": d.Merchant, "asset": "-", "target_asset": "-",
		"currency": d.Currency, "converted": strings.TrimSpace(d.Converted + " " + d.ConvertedCurrency),
		"discount": cmp.Or(d.Discount, "-"), "fee": cmp.Or(d.Fee, "-")}
	if d.Status != "ok" {
		return got
	}
	if cat, ok := c.Category(d.CategoryID); ok {
		got["category"] = cat.Path
	}
	if a, ok := c.Asset(d.AssetID); ok {
		got["asset"] = a.Name
	}
	if a, ok := c.Asset(d.TargetID); ok {
		got["target_asset"] = a.Name
	}
	got["time"] = d.Time.In(cfg.Location()).Format("2006-01-02 15:04")
	return got
}
