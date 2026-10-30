package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/openrouter"
	"ledger-snap/internal/qianji"
	"ledger-snap/internal/recognize"
	"ledger-snap/internal/typesafe"
)

type overrides struct{ extractionModel, decisionModel string }

func (o *overrides) register(fs *flag.FlagSet) {
	fs.StringVar(&o.extractionModel, "extraction-model", "", "图片提取模型")
	fs.StringVar(&o.decisionModel, "decision-model", "", "账户和分类匹配模型")
}
func (o *overrides) apply(cfg *config.Config) {
	if o.extractionModel != "" {
		cfg.Extraction.Model = o.extractionModel
	}
	if o.decisionModel != "" {
		cfg.Decision.Model = o.decisionModel
	}
}

func newRecognizer(cfg *config.Config) (*recognize.Recognizer, error) {
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
	return &recognize.Recognizer{Config: cfg, Decider: decider, Router: router}, nil
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
		drafts, trace, err := rec.Recognize(ctx, recognize.Input{Image: image, UploadTime: upload, Catalog: catalog})
		fmt.Printf("== %s\n", path)
		if err != nil {
			fmt.Println("失败:", err)
			continue
		}
		if len(drafts) == 0 {
			fmt.Println("没有识别到可记账的交易：" + trace.Extraction.Reason)
		}
		for i, draft := range drafts {
			fmt.Printf("%d. %s\n", i+1, summary(draft, catalog))
		}
		fmt.Printf("   提取 %dms · 匹配 %dms · 合计 %dms · $%.5f\n", trace.ExtractionMs, trace.DecisionMs, trace.TotalMs, trace.Cost)
		if *verbose {
			_ = printJSON(map[string]any{"drafts": drafts, "trace": trace})
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
	return fmt.Sprintf("%s %s · %s · %s · %s · %s", d.Kind.Label(), money,
		mark("category", category), mark("asset", asset), mark("time", d.Time.In(d.Time.Location()).Format("01-02 15:04")), d.Merchant)
}
