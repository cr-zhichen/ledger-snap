package openrouter

import (
	"context"
	"encoding/base64"
	"fmt"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// Question is one typed Decisions question. Criteria is a map of option to
// description for choice, {"true","false"} for noul, or a list for score.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

func ChoiceQuestion(instructions string, options []Option) Question {
	criteria := make(map[string]any, len(options))
	for _, o := range options {
		criteria[o.Key] = o.Description
	}
	return Question{Type: "choice", Instructions: instructions, Criteria: criteria}
}

func NoulQuestion(instructions, whenTrue, whenFalse string) Question {
	return Question{Type: "noul", Instructions: instructions, Criteria: map[string]string{"true": whenTrue, "false": whenFalse}}
}

type Option struct {
	Key         string
	Description string
}

type DecisionsRequest struct {
	Model     string              `json:"model"`
	State     []any               `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
}

// Prob is the probability of the selected choice, or of "true" for noul.
func (a Answer) Prob() float64 {
	if a.Type == "noul" {
		return a.Noul
	}
	return a.Probabilities[a.Choice]
}

type DecisionsResponse struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens int     `json:"input_tokens"`
		Cost        float64 `json:"cost"`
	} `json:"usage"`
	ID       string `json:"id"`
	Provider string `json:"provider"`
}

func (c *Client) Decide(ctx context.Context, req DecisionsRequest) (*DecisionsResponse, error) {
	var resp DecisionsResponse
	if err := c.post(ctx, "/api/alpha/decisions", req, &resp); err != nil {
		return nil, err
	}
	for name := range req.Questions {
		if _, ok := resp.Answers[name]; !ok {
			return nil, fmt.Errorf("决策模型没有回答问题 %s", name)
		}
	}
	return &resp, nil
}
