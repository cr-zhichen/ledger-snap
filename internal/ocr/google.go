package ocr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"ledger-snap/internal/config"
)

// Google calls Cloud Vision images:annotate and rebuilds text lines from the
// symbol-level break markers of fullTextAnnotation.
type Google struct {
	cfg  config.Google
	key  string
	http *http.Client
}

func (g *Google) Name() string { return "google" }

type gVertex struct{ X, Y int }
type gBox struct {
	Vertices []gVertex `json:"vertices"`
}
type gSymbol struct {
	Text     string `json:"text"`
	Box      gBox   `json:"boundingBox"`
	Property struct {
		Break struct {
			Type string `json:"type"`
		} `json:"detectedBreak"`
	} `json:"property"`
}

func (g *Google) Recognize(ctx context.Context, image []byte) ([]Line, error) {
	request := map[string]any{"requests": []any{map[string]any{
		"image":        map[string]any{"content": base64.StdEncoding.EncodeToString(image)},
		"features":     []any{map[string]any{"type": g.cfg.Feature}},
		"imageContext": map[string]any{"languageHints": g.cfg.LanguageHints},
	}}}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.Endpoint+"?key="+url.QueryEscape(g.key), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	var result struct {
		Responses []struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Full struct {
				Pages []struct {
					Blocks []struct {
						Paragraphs []struct {
							Words []struct {
								Symbols []gSymbol `json:"symbols"`
							} `json:"words"`
						} `json:"paragraphs"`
					} `json:"blocks"`
				} `json:"pages"`
			} `json:"fullTextAnnotation"`
		} `json:"responses"`
	}
	if err := readJSON(resp, &result); err != nil {
		return nil, err
	}
	if len(result.Responses) == 0 {
		return nil, fmt.Errorf("响应为空")
	}
	r := result.Responses[0]
	if r.Error != nil {
		return nil, fmt.Errorf("%s", r.Error.Message)
	}
	var lines []Line
	var text []rune
	minX, minY, maxX, maxY := 1<<30, 1<<30, -1, -1
	flush := func() {
		if len(text) > 0 && maxX >= 0 {
			lines = append(lines, Line{Text: string(text), X: minX, Y: minY, W: maxX - minX, H: maxY - minY})
		}
		text = text[:0]
		minX, minY, maxX, maxY = 1<<30, 1<<30, -1, -1
	}
	for _, page := range r.Full.Pages {
		for _, block := range page.Blocks {
			for _, para := range block.Paragraphs {
				for _, word := range para.Words {
					for _, s := range word.Symbols {
						text = append(text, []rune(s.Text)...)
						for _, v := range s.Box.Vertices {
							minX, minY = min(minX, v.X), min(minY, v.Y)
							maxX, maxY = max(maxX, v.X), max(maxY, v.Y)
						}
						switch s.Property.Break.Type {
						case "SPACE", "SURE_SPACE":
							text = append(text, ' ')
						case "EOL_SURE_SPACE", "LINE_BREAK", "HYPHEN":
							flush()
						}
					}
				}
			}
			flush()
		}
	}
	return lines, nil
}
