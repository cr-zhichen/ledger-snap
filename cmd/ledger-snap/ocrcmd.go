package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"ledger-snap/internal/config"
	"ledger-snap/internal/ocr"
)

// runOCR prints the lines each OCR provider finds in one image.
func runOCR(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ocr", flag.ExitOnError)
	cfgPath := fs.String("config", "config.toml", "配置文件")
	providers := fs.String("provider", "", "逗号分隔的 OCR 服务，默认用配置里的 provider")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("用法: ledger-snap ocr [--provider volcano,google,baidu] <图片>")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	image, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	names := []string{cfg.OCR.Provider}
	if *providers != "" {
		names = strings.Split(*providers, ",")
	}
	for _, name := range names {
		engine, err := ocr.New(cfg.OCR, name)
		if err != nil {
			return err
		}
		lines, took, err := ocr.Timed(ctx, engine, image)
		if err != nil {
			fmt.Printf("== %s 失败 (%v): %v\n", name, took.Round(1e6), err)
			continue
		}
		fmt.Printf("== %s：%d 行，%v\n", name, len(lines), took.Round(1e6))
		for _, l := range lines {
			fmt.Printf("  [%4d,%4d h=%3d] %s\n", l.X, l.Y, l.H, l.Text)
		}
	}
	return nil
}
