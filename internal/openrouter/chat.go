package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type ChatRequest struct {
	Model          string         `json:"model"`
	Messages       []Message      `json:"messages"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
	Reasoning      map[string]any `json:"reasoning,omitempty"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	// Provider routing; require_parameters keeps requests on providers that
	// honor response_format.
	Provider map[string]any `json:"provider,omitempty"`
}

type ChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		Cost             float64 `json:"cost"`
	} `json:"usage"`
}

// Routing returns provider preferences: only the given provider when one is
// named, and with strict output only providers that honor response_format.
func Routing(provider string, strict bool) map[string]any {
	p := map[string]any{"require_parameters": strict}
	if provider != "" {
		p["order"] = []string{provider}
		p["allow_fallbacks"] = false
	}
	return p
}

// JSONSchemaFormat requests strict structured output.
func JSONSchemaFormat(name string, schema map[string]any) map[string]any {
	return map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": name, "strict": true, "schema": schema}}
}

// ChatJSON sends one request and decodes the reply into out.
func (c *Client) ChatJSON(ctx context.Context, req ChatRequest, out any) (*ChatResponse, error) {
	var resp ChatResponse
	if err := c.post(ctx, "/api/v1/chat/completions", req, &resp); err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return &resp, fmt.Errorf("模型 %s 没有返回内容", req.Model)
	}
	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	// Some providers wrap JSON in a code fence despite response_format.
	content = strings.TrimPrefix(strings.TrimPrefix(content, "```json"), "```")
	content = strings.TrimSpace(strings.TrimSuffix(content, "```"))
	if err := json.Unmarshal([]byte(content), out); err != nil {
		return &resp, fmt.Errorf("模型 %s 输出不是有效 JSON: %w: %s", req.Model, err, snippet([]byte(content)))
	}
	return &resp, nil
}
