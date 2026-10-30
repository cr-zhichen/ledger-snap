package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ledger-snap/internal/app"
	"ledger-snap/internal/config"
	"ledger-snap/internal/server"
	"ledger-snap/internal/store"
)

func newService(cfg *config.Config) (*app.Service, error) {
	rec, err := newRecognizer(cfg)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(filepath.Join(cfg.Server.DataDir, "app.sqlite"))
	if err != nil {
		return nil, err
	}
	var invites []string
	if raw := os.Getenv(cfg.Server.InviteCodesEnv); raw != "" {
		for _, code := range strings.Split(raw, ",") {
			if code = strings.TrimSpace(code); code != "" {
				invites = append(invites, code)
			}
		}
	}
	return &app.Service{Config: cfg, Store: db, Qianji: newQianji(cfg), Recognizer: rec, Invites: invites}, nil
}

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "config.toml", "配置文件")
	listen := fs.String("listen", "", "覆盖 server.listen")
	dataDir := fs.String("data", "", "覆盖 server.data_dir")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Server.Listen = *listen
	}
	if *dataDir != "" {
		cfg.Server.DataDir = *dataDir
	}
	if err := dropRoot(cfg.Server.DataDir); err != nil {
		return err
	}
	svc, err := newService(cfg)
	if err != nil {
		return err
	}
	defer svc.Store.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	svc.Log = log
	if len(svc.Invites) == 0 {
		log.Warn("没有配置邀请码，任何人都无法登录", "env", cfg.Server.InviteCodesEnv)
	}
	go func() {
		for {
			if err := svc.CleanImages(ctx); err != nil {
				log.Error("清理截图失败", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(24 * time.Hour):
			}
		}
	}()
	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           (&server.Server{App: svc, Log: log}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if len(cfg.EnvOverrides) > 0 {
		log.Info("环境变量覆盖了配置", "keys", strings.Join(cfg.EnvOverrides, ","))
	}
	log.Info("服务已启动", "version", version, "listen", cfg.Server.Listen, "extraction", cfg.Extraction.Model, "decision", cfg.Decision.Model)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
