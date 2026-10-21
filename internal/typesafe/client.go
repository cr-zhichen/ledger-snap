// Package typesafe calls TypeSafe's own System One API (api.typesafe.ai)
// with the same questions the OpenRouter Decisions API takes.
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ledger-snap/internal/openrouter"
)

type Client struct {
	baseURL string
	key     string
	// pricePerMTok estimates cost: the API reports tokens, not dollars.
	pricePerMTok float64
	http         *http.Client
}

func New(baseURL, key string, pricePerMTok float64, timeout time.Duration) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), key: key, pricePerMTok: pricePerMTok, http: &http.Client{Timeout: timeout}}
}

type asset struct {
	Type       string `json:"type"`
	Format     string `json:"format"`
	DataBase64 string `json:"data_base64"`
}

// Decide sends a Decisions-style request to /v1/systemone. Inline image parts
// in the state are moved into "assets" and replaced by placeholders, which is
// how this API takes images.
func (c *Client) Decide(ctx context.Context, req openrouter.DecisionsRequest) (*openrouter.DecisionsResponse, error) {
	assets := map[string]asset{}
	state := make([]any, 0, len(req.State))
	for _, item := range req.State {
		if url, ok := imageURL(item); ok {
			format, data, err := splitDataURL(url)
			if err != nil {
				return nil, err
			}
			ref := fmt.Sprintf("[image-%d]", len(assets)+1)
			assets[ref] = asset{Type: "image", Format: format, DataBase64: data}
			state = append(state, ref)
			continue
		}
		state = append(state, item)
	}
	body := map[string]any{"model": req.Model, "state": state, "questions": req.Questions}
	if len(assets) > 0 {
		body["assets"] = assets
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/systemone", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.key)
	httpReq.Header.Set("Content-Type", "application/json")
	if len(assets) > 0 {
		httpReq.Header.Set("TypeSafe-Danger-Zone-Images", "true")
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("TypeSafe HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	var out struct {
		Model   string                       `json:"model"`
		Answers map[string]openrouter.Answer `json:"answers"`
		Usage   struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("TypeSafe 响应无法解析: %w: %s", err, snippet(raw))
	}
	for name := range req.Questions {
		if _, ok := out.Answers[name]; !ok {
			return nil, fmt.Errorf("决策模型没有回答问题 %s", name)
		}
	}
	res := &openrouter.DecisionsResponse{Model: out.Model, Answers: out.Answers, Provider: "TypeSafe"}
	res.Usage.InputTokens = out.Usage.InputTokens
	res.Usage.Cost = float64(out.Usage.InputTokens) * c.pricePerMTok / 1e6
	return res, nil
}

func imageURL(item any) (string, bool) {
	m, ok := item.(map[string]any)
	if !ok || m["type"] != "image_url" {
		return "", false
	}
	inner, _ := m["image_url"].(map[string]any)
	url, ok := inner["url"].(string)
	return url, ok
}

// splitDataURL turns "data:image/jpeg;base64,..." into ("jpg", "...").
func splitDataURL(url string) (string, string, error) {
	head, data, ok := strings.Cut(url, ";base64,")
	if !ok {
		return "", "", fmt.Errorf("图片不是 base64 data URL")
	}
	switch strings.TrimPrefix(head, "data:") {
	case "image/jpeg", "image/jpg":
		return "jpg", data, nil
	case "image/png":
		return "png", data, nil
	}
	return "", "", fmt.Errorf("TypeSafe 只接受 PNG 或 JPEG 图片")
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if r := []rune(s); len(r) > 400 {
		return string(r[:400]) + "…"
	}
	return s
}
