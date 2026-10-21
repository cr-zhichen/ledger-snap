// Package ocr wraps the Volcano Engine, Google Cloud Vision and Baidu text
// recognition APIs behind one line-oriented interface.
package ocr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"ledger-snap/internal/config"
)

// Line is one recognized text line in image pixel coordinates.
type Line struct {
	Text string  `json:"text"`
	X    int     `json:"x"`
	Y    int     `json:"y"`
	W    int     `json:"w"`
	H    int     `json:"h"`
	Prob float64 `json:"prob,omitempty"`
}

type Engine interface {
	Name() string
	Recognize(ctx context.Context, image []byte) ([]Line, error)
}

func New(cfg config.OCR, provider string) (Engine, error) {
	if provider == "" {
		provider = cfg.Provider
	}
	client := &http.Client{Timeout: cfg.Timeout.Duration}
	switch provider {
	case "volcano":
		ak, err := config.Secret(cfg.Volcano.AccessKeyEnv)
		if err != nil {
			return nil, err
		}
		sk, err := config.Secret(cfg.Volcano.SecretKeyEnv)
		if err != nil {
			return nil, err
		}
		return &Volcano{cfg: cfg.Volcano, ak: ak, sk: sk, http: client}, nil
	case "google":
		key, err := config.Secret(cfg.Google.APIKeyEnv)
		if err != nil {
			return nil, err
		}
		return &Google{cfg: cfg.Google, key: key, http: client}, nil
	case "baidu":
		ak, err := config.Secret(cfg.Baidu.APIKeyEnv)
		if err != nil {
			return nil, err
		}
		sk, err := config.Secret(cfg.Baidu.SecretKeyEnv)
		if err != nil {
			return nil, err
		}
		return &Baidu{cfg: cfg.Baidu, ak: ak, sk: sk, http: client}, nil
	}
	return nil, fmt.Errorf("未知的 OCR 服务 %q", provider)
}

// SortLines orders lines top to bottom, then left to right within a row.
func SortLines(lines []Line) {
	sort.SliceStable(lines, func(i, j int) bool {
		a, b := lines[i], lines[j]
		if overlapY(a, b) {
			return a.X < b.X
		}
		return a.Y < b.Y
	})
}

func overlapY(a, b Line) bool {
	top, bottom := max(a.Y, b.Y), min(a.Y+a.H, b.Y+b.H)
	return bottom-top > min(a.H, b.H)/2
}

// HTTPError is a non-2xx response from an OCR service.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

// retryable reports rate limiting and server errors, which are worth retrying.
func retryable(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && (he.Status == http.StatusTooManyRequests || he.Status >= 500)
}

func readJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Status: resp.StatusCode, Body: truncate(string(body), 300)}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("解析响应: %w: %s", err, truncate(string(body), 300))
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Timed runs one recognition and reports its latency. Rate limiting and server
// errors are retried twice with a short backoff; Volcano's trial quota, for
// one, allows a single request per second.
func Timed(ctx context.Context, e Engine, image []byte) ([]Line, time.Duration, error) {
	start := time.Now()
	lines, err := e.Recognize(ctx, image)
	for attempt := 1; attempt <= 2 && retryable(err); attempt++ {
		select {
		case <-ctx.Done():
			return nil, time.Since(start), ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
		lines, err = e.Recognize(ctx, image)
	}
	if err != nil {
		return nil, time.Since(start), fmt.Errorf("%s OCR: %w", e.Name(), err)
	}
	SortLines(lines)
	return lines, time.Since(start), nil
}
