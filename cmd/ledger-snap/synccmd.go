package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"ledger-snap/internal/app"
	"ledger-snap/internal/config"
	"ledger-snap/internal/store"
)

// runSyncBills syncs the bill cache of every user, or of one account, now.
// It can run next to the server: a sync that races with it starts over.
func runSyncBills(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync-bills", flag.ExitOnError)
	cfgPath := fs.String("config", "config.toml", "配置文件")
	dataDir := fs.String("data", "", "覆盖 server.data_dir")
	account := fs.String("account", "", "只同步这个钱迹账号，默认同步全部用户")
	full := fs.Bool("full", false, "清空后重新拉取全部账单")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *dataDir != "" {
		cfg.Server.DataDir = *dataDir
	}
	if err := dropRoot(cfg.Server.DataDir); err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(cfg.Server.DataDir, "app.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()
	svc := &app.Service{Config: cfg, Store: db, Qianji: newQianji(cfg), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	users, err := db.Users(ctx)
	if err != nil {
		return err
	}
	synced, failed := 0, 0
	for _, u := range users {
		if *account != "" && u.Account != *account {
			continue
		}
		synced++
		r, err := svc.SyncBills(ctx, u.UID, *full)
		if err != nil {
			failed++
			fmt.Printf("%s：同步失败：%v\n", u.Account, err)
			continue
		}
		total, err := db.CountBills(ctx, u.UID)
		if err != nil {
			return err
		}
		mode := "增量"
		if r.Full {
			mode = "全量"
		}
		fmt.Printf("%s：%s同步 %d 页，新增或修改 %d 笔，删除 %d 笔，缓存共 %d 笔（%v）\n",
			u.Account, mode, r.Pages, r.Changes, r.Deletes, total, r.Took.Round(time.Millisecond))
	}
	switch {
	case *account != "" && synced == 0:
		return fmt.Errorf("没有登录过的账号 %s", *account)
	case failed > 0:
		return errors.New("有账号同步失败")
	}
	return nil
}
