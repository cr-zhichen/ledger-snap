// Package openrouter calls OpenRouter's Decisions API and Chat Completions API.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	key     string
	appName string
	http    *http.Client
}

func New(baseURL, key, appName string, timeout time.Duration) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), key: key, appName: appName, http: &http.Client{Timeout: timeout}}
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	if c.appName != "" {
		req.Header.Set("X-Title", c.appName)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OpenRouter %s HTTP %d: %s", path, resp.StatusCode, snippet(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("OpenRouter %s 响应无法解析: %w: %s", path, err, snippet(raw))
	}
	return nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if r := []rune(s); len(r) > 400 {
		return string(r[:400]) + "…"
	}
	return s
}

// ImagePart is the content part both APIs accept for an inline image.
func ImagePart(jpeg []byte) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64," + b64(jpeg)}}
}
