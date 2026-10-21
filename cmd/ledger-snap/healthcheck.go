package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"time"
)

// runHealthcheck probes /healthz; it backs the container HEALTHCHECK, since
// the distroless runtime image has no curl or wget.
func runHealthcheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	url := fs.String("url", "http://127.0.0.1:8080/healthz", "健康检查地址")
	_ = fs.Parse(args)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("健康检查返回 HTTP %d", resp.StatusCode)
	}
	return nil
}
