package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/ocr"
	"ledger-snap/internal/openrouter"
	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
	"ledger-snap/internal/typesafe"
)

type overrides struct {
	provider, decisionProvider, decisionModel, fallbackModel string
	noImage, noFallback, forceFallback                       bool
}

func (o *overrides) register(fs *flag.FlagSet) {
	fs.StringVar(&o.provider, "provider", "", "覆盖 ocr.provider")
	fs.StringVar(&o.decisionProvider, "decision-provider", "", "覆盖 decision.provider（openrouter 或 typesafe）")
	fs.StringVar(&o.decisionModel, "decision-model", "", "覆盖 decision.model")
	fs.StringVar(&o.fallbackModel, "fallback-model", "", "覆盖 fallback.model")
	fs.BoolVar(&o.noImage, "no-image", false, "决策模型只看 OCR 文字，不看截图")
	fs.BoolVar(&o.noFallback, "no-fallback", false, "关闭视觉 LLM 兜底")
	fs.BoolVar(&o.forceFallback, "force-fallback", false, "把阈值调到最高，让每张截图都走兜底")
}

func (o *overrides) apply(cfg *config.Config) {
	if o.provider != "" {
		cfg.OCR.Provider = o.provider
	}
	if o.decisionProvider != "" {
		cfg.Decision.Provider = o.decisionProvider
	}
	if o.decisionModel != "" {
		cfg.Decision.Model = o.decisionModel
	}
	if o.fallbackModel != "" {
		cfg.Fallback.Model = o.fallbackModel
	}
	if o.noImage {
		cfg.Decision.SendImage = false
	}
	if o.noFallback {
		cfg.Fallback.Enabled = false
	}
	if o.forceFallback {
		cfg.Decision.Thresholds.Transaction, cfg.Decision.Thresholds.Type = 1.01, 1.01
	}
}

func newRecognizer(cfg *config.Config) (*recognize.Recognizer, error) {
	engine, err := ocr.New(cfg.OCR, "")
	if err != nil {
		return nil, err
	}
	key, err := config.Secret(cfg.OpenRouter.APIKeyEnv)
	if err != nil {
		return nil, err
	}
	router := openrouter.New(cfg.OpenRouter.BaseURL, key, cfg.OpenRouter.AppName, cfg.OpenRouter.Timeout.Duration)
	var decider recognize.Decider = router
	if cfg.Decision.Provider == "typesafe" {
		tsKey, err := config.Secret(cfg.TypeSafe.APIKeyEnv)
		if err != nil {
			return nil, err
		}
		decider = typesafe.New(cfg.TypeSafe.BaseURL, tsKey, cfg.TypeSafe.InputPricePerMTok, cfg.TypeSafe.Timeout.Duration)
	}
	return &recognize.Recognizer{Config: cfg, OCR: engine, Decider: decider, Router: router}, nil
}

// testCatalog logs in with the test account and loads its catalog.
func testCatalog(ctx context.Context, cfg *config.Config) (qianji.Catalog, error) {
	account, err := config.Secret("QIANJI_TEST_ACCOUNT")
	if err != nil {
		return qianji.Catalog{}, err
	}
	digest, err := config.Secret("QIANJI_TEST_PASSWORD_MD5")
	if err != nil {
		return qianji.Catalog{}, err
	}
	c := newQianji(cfg)
	s, err := c.Login(ctx, account, digest, randomDevice())
	if err != nil {
		return qianji.Catalog{}, err
	}
	cats, err := c.Categories(ctx, s, qianji.NoID, -1)
	if err != nil {
		return qianji.Catalog{}, err
	}
	assets, err := c.Assets(ctx, s, 0)
	if err != nil {
		return qianji.Catalog{}, err
	}
	return qianji.ParseCatalog(cats, assets)
}

// runRecognize prints the draft for one or more screenshots without writing.
func runRecognize(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("recognize", flag.ExitOnError)
	cfgPath := fs.String("config", "config.toml", "配置文件")
	at := fs.String("time", "", "上传时间，格式 2006-01-02 15:04，默认现在")
	verbose := fs.Bool("v", false, "打印完整的识别过程")
	var o overrides
	o.register(fs)
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("用法: ledger-snap recognize [参数] <图片>...")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	o.apply(cfg)
	upload := time.Now()
	if *at != "" {
		if upload, err = time.ParseInLocation("2006-01-02 15:04", *at, cfg.Location()); err != nil {
			return err
		}
	}
	rec, err := newRecognizer(cfg)
	if err != nil {
		return err
	}
	catalog, err := testCatalog(ctx, cfg)
	if err != nil {
		return err
	}
	for _, path := range fs.Args() {
		image, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		draft, trace, err := rec.Recognize(ctx, recognize.Input{Image: image, UploadTime: upload, Catalog: catalog})
		fmt.Printf("== %s\n", path)
		if err != nil {
			fmt.Println("失败:", err)
			continue
		}
		fmt.Println(summary(draft, catalog))
		fmt.Printf("   OCR %dms · 决策 %dms · 兜底 %dms · 合计 %dms · $%.5f", trace.OCRMs, trace.DecisionMs, trace.FallbackMs, trace.TotalMs, trace.Cost)
		if trace.FallbackWhy != "" {
			fmt.Printf(" · 兜底原因：%s", trace.FallbackWhy)
		}
		if trace.DecisionError != "" {
			fmt.Printf(" · 决策错误：%s", trace.DecisionError)
		}
		if trace.FallbackError != "" {
			fmt.Printf(" · 兜底错误：%s", trace.FallbackError)
		}
		fmt.Println()
		if *verbose {
			_ = printJSON(trace)
		}
	}
	return nil
}

// summary renders a draft the way the shortcut menu shows it.
func summary(d *recognize.Draft, catalog qianji.Catalog) string {
	if d.Status != "ok" {
		return "不记账：" + d.Reason
	}
	mark := func(field, text string) string {
		for _, f := range d.Uncertain {
			if f == field {
				return text + "?"
			}
		}
		return text
	}
	category, asset := "未分类", "无账户"
	if c, ok := catalog.Category(d.CategoryID); ok {
		category = c.Path
	}
	if a, ok := catalog.Asset(d.AssetID); ok {
		asset = a.Name
	}
	if a, ok := catalog.Asset(d.TargetID); ok {
		asset += " → " + a.Name
	}
	money := strings.TrimSpace(d.Money + " " + d.Currency)
	if d.Converted != "" {
		money += " / " + strings.TrimSpace(d.Converted+" "+d.ConvertedCurrency)
	}
	return fmt.Sprintf("%s %s · %s · %s · %s · %s [%s]", d.Kind.Label(), money,
		mark("category", category), mark("asset", asset), mark("time", d.Time.In(d.Time.Location()).Format("01-02 15:04")), d.Merchant, d.Source)
}
