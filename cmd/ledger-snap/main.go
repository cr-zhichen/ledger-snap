package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	// Embed the zone database so recognize.timezone works in minimal images.
	_ "time/tzdata"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

var commands = map[string]struct {
	run  func(ctx context.Context, args []string) error
	help string
}{
	"qianji-smoke": {runQianjiSmoke, "用测试账号登录钱迹，读目录和账单，并新增、修改、删除一笔测试账单"},
	"ocr":          {runOCR, "用一个或多个 OCR 服务识别图片并打印文字行"},
	"recognize":    {runRecognize, "用测试账号的目录识别截图，只打印结果，不写入钱迹"},
	"serve":        {runServe, "启动给快捷指令调用的 HTTP 服务"},
	"sync-bills":   {runSyncBills, "立即从钱迹同步账单缓存；--full 全量重建，--account 只同步一个账号"},
	"config":       {runConfig, "列出全部配置项、覆盖它的环境变量和当前值"},
	"eval":         {runEval, "按 labels.json 评测识别准确率、耗时和费用，不写入钱迹"},
	"seed-assets":  {runSeedAssets, "在测试账号里新建一批容易混淆的账户（已存在的跳过）"},
	"healthcheck":  {runHealthcheck, "检查服务的 /healthz，给容器健康检查用"},
	"shortcut":     {runShortcut, "生成并签名 iOS 快捷指令（签名需要 macOS）"},
	"version":      {func(context.Context, []string) error { fmt.Println(version); return nil }, "打印版本号"},
}

func main() {
	if len(os.Args) < 2 || commands[os.Args[1]].run == nil {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := commands[os.Args[1]].run(ctx, os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: ledger-snap <命令> [参数]\n\n命令:")
	for name, c := range commands {
		fmt.Fprintf(os.Stderr, "  %-14s %s\n", name, c.help)
	}
}
