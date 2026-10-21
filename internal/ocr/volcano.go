package ocr

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ledger-snap/internal/config"
	"ledger-snap/internal/imgutil"
)

// Volcano calls Volcano Engine visual OCR: MultiLanguageOCR (recommended) or
// OCRNormal, chosen by config.
type Volcano struct {
	cfg    config.Volcano
	ak, sk string
	http   *http.Client
}

func (v *Volcano) Name() string { return "volcano" }

func (v *Volcano) Recognize(ctx context.Context, image []byte) ([]Line, error) {
	// MultiLanguageOCR rejects images over 2048 px per side; send a smaller
	// copy and map coordinates back to the original.
	scale := 1.0
	if v.cfg.MaxSide > 0 {
		w, h, err := imgutil.Size(image)
		if err != nil {
			return nil, err
		}
		if long := max(w, h); long > v.cfg.MaxSide {
			if image, err = imgutil.Fit(image, v.cfg.MaxSide, 0, 90); err != nil {
				return nil, err
			}
			scale = float64(long) / float64(v.cfg.MaxSide)
		}
	}
	form := url.Values{"image_base64": {base64.StdEncoding.EncodeToString(image)}}
	if v.cfg.FilterThresh != "" {
		form.Set("filter_thresh", v.cfg.FilterThresh)
	}
	body := form.Encode()
	query := "Action=" + v.cfg.Action + "&Version=" + v.cfg.Version
	endpoint, err := url.Parse(v.cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.cfg.Endpoint+"/?"+query, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	v.signRequest(req, endpoint.Host, query, body, time.Now().UTC())
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err
	}
	var result struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		Meta    struct {
			Error struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"ResponseMetadata"`
	}
	if err := readJSON(resp, &result); err != nil {
		return nil, err
	}
	if result.Meta.Error.Code != "" {
		return nil, fmt.Errorf("%s: %s", result.Meta.Error.Code, result.Meta.Error.Message)
	}
	if result.Code != 10000 {
		return nil, fmt.Errorf("code %d: %s", result.Code, result.Message)
	}
	var lines []Line
	if v.cfg.Action == "MultiLanguageOCR" {
		lines, err = parseMultiLanguage(result.Data)
	} else {
		lines, err = parseNormal(result.Data)
	}
	if err != nil {
		return nil, err
	}
	if scale != 1 {
		for i := range lines {
			l := &lines[i]
			l.X, l.Y = int(float64(l.X)*scale), int(float64(l.Y)*scale)
			l.W, l.H = int(float64(l.W)*scale), int(float64(l.H)*scale)
		}
	}
	return lines, nil
}

func parseMultiLanguage(data json.RawMessage) ([]Line, error) {
	var d struct {
		Infos []struct {
			Text string          `json:"text"`
			Rect [][]float64     `json:"rect"`
			Prob json.RawMessage `json:"prob"` // number or numeric string
		} `json:"ocr_infos"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("解析 ocr_infos: %w", err)
	}
	lines := make([]Line, 0, len(d.Infos))
	for _, info := range d.Infos {
		l := Line{Text: info.Text}
		minX, minY, maxX, maxY := 1e9, 1e9, -1.0, -1.0
		for _, p := range info.Rect {
			if len(p) == 2 {
				minX, minY, maxX, maxY = min(minX, p[0]), min(minY, p[1]), max(maxX, p[0]), max(maxY, p[1])
			}
		}
		if maxX >= 0 {
			l.X, l.Y, l.W, l.H = int(minX), int(minY), int(maxX-minX), int(maxY-minY)
		}
		l.Prob, _ = strconv.ParseFloat(strings.Trim(string(info.Prob), `"`), 64)
		lines = append(lines, l)
	}
	return lines, nil
}

func parseNormal(data json.RawMessage) ([]Line, error) {
	var d struct {
		Texts []string `json:"line_texts"`
		Rects []struct {
			X, Y, Width, Height int
		} `json:"line_rects"`
		Probs []float64 `json:"line_probs"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("解析 line_texts: %w", err)
	}
	lines := make([]Line, 0, len(d.Texts))
	for i, text := range d.Texts {
		l := Line{Text: text}
		if i < len(d.Rects) {
			r := d.Rects[i]
			l.X, l.Y, l.W, l.H = r.X, r.Y, r.Width, r.Height
		}
		if i < len(d.Probs) {
			l.Prob = d.Probs[i]
		}
		lines = append(lines, l)
	}
	return lines, nil
}

// signRequest applies Volcano Engine's HMAC-SHA256 request signature.
func (v *Volcano) signRequest(req *http.Request, host, query, body string, now time.Time) {
	xdate := now.Format("20060102T150405Z")
	short := now.Format("20060102")
	payloadHash := sha256hex(body)
	const contentType = "application/x-www-form-urlencoded"
	signed := "content-type;host;x-content-sha256;x-date"
	canonical := strings.Join([]string{
		http.MethodPost, "/", query,
		"content-type:" + contentType + "\nhost:" + host + "\nx-content-sha256:" + payloadHash + "\nx-date:" + xdate + "\n",
		signed, payloadHash,
	}, "\n")
	scope := short + "/" + v.cfg.Region + "/" + v.cfg.Service + "/request"
	toSign := "HMAC-SHA256\n" + xdate + "\n" + scope + "\n" + sha256hex(canonical)
	key := hmacSHA256([]byte(v.sk), short)
	key = hmacSHA256(key, v.cfg.Region)
	key = hmacSHA256(key, v.cfg.Service)
	key = hmacSHA256(key, "request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Host = host
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Date", xdate)
	req.Header.Set("X-Content-Sha256", payloadHash)
	req.Header.Set("Authorization", "HMAC-SHA256 Credential="+v.ak+"/"+scope+", SignedHeaders="+signed+", Signature="+signature)
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
