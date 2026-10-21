package main

import (
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

// label is the expected result for one screenshot. Empty fields are not checked.
type label struct {
	Status   string `json:"status"` // ok | rejected
	Kind     string `json:"kind"`
	Money    string `json:"money"`
	Category string `json:"category"` // category path, e.g. "三餐"
	Asset    string `json:"asset"`    // asset name, or "-" for none
	Target   string `json:"target_asset"`
	Time     string `json:"time"`     // 2006-01-02 15:04
	Merchant string `json:"merchant"` // substring of the merchant
}

type labelFile struct {
	UploadTime string           `json:"upload_time"` // 2006-01-02 15:04
	Images     map[string]label `json:"images"`
}

// runEval checks recognition against labels.json without writing to Qianji.
func runEval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	cfgPath := fs.String("config", "config.toml", "配置文件")
	labelsPath := fs.String("labels", "testdata/screens/labels.json", "标注文件，图片路径相对于它所在的目录")
	out := fs.String("out", "", "把每张截图的完整识别过程写到这个目录")
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
	names := make([]string, 0, len(lf.Images))
	for name := range lf.Images {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Printf("OCR=%s 决策=%s 兜底=%s（启用=%v）\n\n", cfg.OCR.Provider, cfg.Decision.Model, cfg.Fallback.Model, cfg.Fallback.Enabled)
	fields := []string{"status", "kind", "money", "category", "asset", "target_asset", "time", "merchant"}
	right, checked := map[string]int{}, map[string]int{}
	var totals []int64
	cost, fallbacks := 0.0, 0
	for _, name := range names {
		want := lf.Images[name]
		image, err := os.ReadFile(filepath.Join(filepath.Dir(*labelsPath), name))
		if err != nil {
			return err
		}
		draft, trace, err := rec.Recognize(ctx, recognize.Input{Image: image, UploadTime: upload, Catalog: catalog})
		if err != nil {
			fmt.Printf("✗ %s 失败：%v\n", name, err)
			continue
		}
		totals = append(totals, trace.TotalMs)
		cost += trace.Cost
		if trace.FallbackWhy != "" {
			fallbacks++
		}
		got := actual(draft, catalog, cfg)
		var wrong []string
		for _, f := range fields {
			w := field(want, f)
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
			} else {
				wrong = append(wrong, fmt.Sprintf("%s=%q（应为 %q）", f, got[f], w))
			}
		}
		mark := "✓"
		if len(wrong) > 0 {
			mark = "✗"
		}
		fmt.Printf("%s %-28s %5dms %s\n", mark, name, trace.TotalMs, strings.Join(wrong, "，"))
		if *out != "" {
			_ = writeJSON(filepath.Join(*out, strings.TrimSuffix(name, filepath.Ext(name))+".json"), map[string]any{"draft": draft, "trace": trace})
		}
	}
	fmt.Println("\n字段准确率：")
	for _, f := range fields {
		if checked[f] > 0 {
			fmt.Printf("  %-12s %d/%d\n", f, right[f], checked[f])
		}
	}
	slices.Sort(totals)
	if n := len(totals); n > 0 {
		fmt.Printf("耗时 p50=%dms p95=%dms；走兜底 %d/%d；总费用 $%.5f\n", totals[n/2], totals[min(n-1, n*95/100)], fallbacks, n, cost)
	}
	return nil
}

func field(l label, name string) string {
	return map[string]string{"status": l.Status, "kind": l.Kind, "money": l.Money, "category": l.Category, "asset": l.Asset, "target_asset": l.Target, "time": l.Time, "merchant": l.Merchant}[name]
}

func actual(d *recognize.Draft, c qianji.Catalog, cfg *config.Config) map[string]string {
	got := map[string]string{"status": d.Status, "kind": string(d.Kind), "money": d.Money, "merchant": d.Merchant, "asset": "-", "target_asset": "-"}
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
