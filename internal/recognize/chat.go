package recognize

import (
	"encoding/json"

	"ledger-snap/internal/config"
	"ledger-snap/internal/openrouter"
)

// chatModel is how to call a chat model for JSON output.
type chatModel struct {
	Model, Provider, ReasoningEffort string
	// SchemaInPrompt puts the JSON schema in the prompt, for providers that
	// cannot be held to one.
	SchemaInPrompt bool
	MaxTokens      int
}

func rowsModel(c config.Rows) chatModel {
	return chatModel{Model: c.Model, Provider: c.Provider, ReasoningEffort: c.ReasoningEffort, SchemaInPrompt: c.SchemaInPrompt, MaxTokens: c.MaxTokens}
}

func fallbackModel(c config.Fallback) chatModel {
	return chatModel{Model: c.Model, Provider: c.Provider, ReasoningEffort: c.ReasoningEffort, SchemaInPrompt: c.SchemaInPrompt, MaxTokens: c.MaxTokens}
}

// request asks for JSON matching schema about the text and, when given, an
// image.
func (m chatModel) request(name string, schema map[string]any, text string, image []byte) openrouter.ChatRequest {
	if m.SchemaInPrompt {
		b, _ := json.Marshal(schema)
		text += "\n\n只输出一个 JSON 对象，符合这个 JSON Schema：\n" + string(b)
	}
	content := []any{map[string]any{"type": "text", "text": text}}
	if image != nil {
		content = append(content, openrouter.ImagePart(image))
	}
	req := openrouter.ChatRequest{
		Model:     m.Model,
		Messages:  []openrouter.Message{{Role: "user", Content: content}},
		MaxTokens: m.MaxTokens,
		Provider:  openrouter.Routing(m.Provider, !m.SchemaInPrompt),
	}
	if !m.SchemaInPrompt {
		req.ResponseFormat = openrouter.JSONSchemaFormat(name, schema)
	}
	if m.ReasoningEffort != "" {
		req.Reasoning = map[string]any{"effort": m.ReasoningEffort, "exclude": true}
	}
	return req
}
