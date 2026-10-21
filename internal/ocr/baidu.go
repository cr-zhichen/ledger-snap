package ocr

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ledger-snap/internal/config"
)

// Baidu calls the accurate/general OCR endpoints that return line locations.
type Baidu struct {
	cfg    config.Baidu
	ak, sk string
	http   *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (b *Baidu) Name() string { return "baidu" }

func (b *Baidu) accessToken(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.token != "" && time.Now().Before(b.expires) {
		return b.token, nil
	}
	q := url.Values{"grant_type": {"client_credentials"}, "client_id": {b.ak}, "client_secret": {b.sk}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.TokenURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return "", err
	}
	var result struct {
		Token     string `json:"access_token"`
		ExpiresIn int64  `json:"expires_in"`
		Error     string `json:"error"`
		ErrorDesc string `json:"error_description"`
	}
	if err := readJSON(resp, &result); err != nil {
		return "", err
	}
	if result.Token == "" {
		return "", fmt.Errorf("获取 access_token 失败: %s %s", result.Error, result.ErrorDesc)
	}
	b.token = result.Token
	// Refresh a day early; tokens normally last 30 days.
	b.expires = time.Now().Add(time.Duration(result.ExpiresIn)*time.Second - 24*time.Hour)
	return b.token, nil
}

func (b *Baidu) Recognize(ctx context.Context, image []byte) ([]Line, error) {
	token, err := b.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{"image": {base64.StdEncoding.EncodeToString(image)}, "probability": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.Endpoint+"?access_token="+url.QueryEscape(token), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	var result struct {
		ErrorCode int    `json:"error_code"`
		ErrorMsg  string `json:"error_msg"`
		Words     []struct {
			Words    string `json:"words"`
			Location struct {
				Top, Left, Width, Height int
			} `json:"location"`
			Probability struct {
				Average float64 `json:"average"`
			} `json:"probability"`
		} `json:"words_result"`
	}
	if err := readJSON(resp, &result); err != nil {
		return nil, err
	}
	if result.ErrorCode != 0 {
		return nil, fmt.Errorf("error_code %d: %s", result.ErrorCode, result.ErrorMsg)
	}
	lines := make([]Line, 0, len(result.Words))
	for _, w := range result.Words {
		l := w.Location
		lines = append(lines, Line{Text: w.Words, X: l.Left, Y: l.Top, W: l.Width, H: l.Height, Prob: w.Probability.Average})
	}
	return lines, nil
}
