// Package config loads config.toml. Secrets never live in the file: each
// secret field names the environment variable that holds it.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("无效的时长 %q: %w", text, err)
	}
	d.Duration = v
	return nil
}

type Config struct {
	Server     Server     `toml:"server"`
	Qianji     Qianji     `toml:"qianji"`
	OCR        OCR        `toml:"ocr"`
	OpenRouter OpenRouter `toml:"openrouter"`
	Decision   Decision   `toml:"decision"`
	TypeSafe   TypeSafe   `toml:"typesafe"`
	Fallback   Fallback   `toml:"fallback"`
	Correction Correction `toml:"correction"`
	Recognize  Recognize  `toml:"recognize"`
}

type Server struct {
	Listen             string `toml:"listen"`
	DataDir            string `toml:"data_dir"`
	InviteCodesEnv     string `toml:"invite_codes_env"`
	MaxUploadMB        int64  `toml:"max_upload_mb"`
	ImageRetentionDays int    `toml:"image_retention_days"`
}

type Qianji struct {
	BaseURL     string   `toml:"base_url"`
	Timeout     Duration `toml:"timeout"`
	VersionCode int      `toml:"version_code"`
	VersionName string   `toml:"version_name"`
	Market      string   `toml:"market"`
	OSVersion   string   `toml:"os_version"`
	DeviceBrand string   `toml:"device_brand"`
	DeviceName  string   `toml:"device_name"`
	CatalogTTL  Duration `toml:"catalog_ttl"`
}

type OCR struct {
	Provider string   `toml:"provider"`
	Timeout  Duration `toml:"timeout"`
	Volcano  Volcano  `toml:"volcano"`
	Google   Google   `toml:"google"`
	Baidu    Baidu    `toml:"baidu"`
}

type Volcano struct {
	Endpoint     string `toml:"endpoint"`
	Action       string `toml:"action"`
	Version      string `toml:"version"`
	Region       string `toml:"region"`
	Service      string `toml:"service"`
	AccessKeyEnv string `toml:"access_key_env"`
	SecretKeyEnv string `toml:"secret_key_env"`
	FilterThresh string `toml:"filter_thresh"`
	MaxSide      int    `toml:"max_side"`
}

type Google struct {
	Endpoint      string   `toml:"endpoint"`
	APIKeyEnv     string   `toml:"api_key_env"`
	Feature       string   `toml:"feature"`
	LanguageHints []string `toml:"language_hints"`
}

type Baidu struct {
	TokenURL     string `toml:"token_url"`
	Endpoint     string `toml:"endpoint"`
	APIKeyEnv    string `toml:"api_key_env"`
	SecretKeyEnv string `toml:"secret_key_env"`
}

type OpenRouter struct {
	BaseURL   string   `toml:"base_url"`
	APIKeyEnv string   `toml:"api_key_env"`
	Timeout   Duration `toml:"timeout"`
	AppName   string   `toml:"app_name"`
}

type TypeSafe struct {
	BaseURL   string   `toml:"base_url"`
	APIKeyEnv string   `toml:"api_key_env"`
	Timeout   Duration `toml:"timeout"`
	// InputPricePerMTok is only used to estimate cost in traces.
	InputPricePerMTok float64 `toml:"input_price_per_mtok"`
}

type Decision struct {
	Provider      string     `toml:"provider"` // openrouter | typesafe
	Model         string     `toml:"model"`
	SendImage     bool       `toml:"send_image"`
	ImageMaxSide  int        `toml:"image_max_side"`
	ImageMinSide  int        `toml:"image_min_side"`
	JPEGQuality   int        `toml:"jpeg_quality"`
	MaxCandidates int        `toml:"max_candidates"`
	Thresholds    Thresholds `toml:"thresholds"`
}

// Thresholds are the minimum probabilities for writing without falling back.
type Thresholds struct {
	Transaction float64 `toml:"transaction"`
	Type        float64 `toml:"type"`
	Amount      float64 `toml:"amount"`
	Time        float64 `toml:"time"`
	Category    float64 `toml:"category"`
	Asset       float64 `toml:"asset"`
}

type Fallback struct {
	Enabled         bool   `toml:"enabled"`
	Model           string `toml:"model"`
	ReasoningEffort string `toml:"reasoning_effort"`
	MaxTokens       int    `toml:"max_tokens"`
	ImageMaxSide    int    `toml:"image_max_side"`
}

type Correction struct {
	Model           string `toml:"model"`
	ReasoningEffort string `toml:"reasoning_effort"`
	MaxTokens       int    `toml:"max_tokens"`
}

type Recognize struct {
	Timezone        string   `toml:"timezone"`
	DedupWindow     Duration `toml:"dedup_window"`
	StaleScreenshot Duration `toml:"stale_screenshot"`
	// ConfirmUncertain holds back drafts with low-confidence fields until the
	// user confirms them.
	ConfirmUncertain bool `toml:"confirm_uncertain"`
}

func Load(path string) (*Config, error) {
	var c Config
	meta, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	if keys := meta.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("配置 %s 有未知字段: %v", path, keys)
	}
	if c.Decision.Provider != "openrouter" && c.Decision.Provider != "typesafe" {
		return nil, fmt.Errorf("decision.provider 只能是 openrouter 或 typesafe")
	}
	// Jev allows 255 options per choice question; one is the "none" option.
	if c.Decision.MaxCandidates < 1 || c.Decision.MaxCandidates > 254 {
		return nil, fmt.Errorf("decision.max_candidates 必须在 1 到 254 之间")
	}
	if _, err := time.LoadLocation(c.Recognize.Timezone); err != nil {
		return nil, fmt.Errorf("recognize.timezone 无效: %w", err)
	}
	return &c, nil
}

// Secret reads the environment variable named by a config field.
func Secret(env string) (string, error) {
	if env == "" {
		return "", fmt.Errorf("配置里没有指定环境变量名")
	}
	v := strings.TrimSpace(os.Getenv(env))
	if v == "" {
		return "", fmt.Errorf("环境变量 %s 未设置", env)
	}
	return v, nil
}

func (c *Config) Location() *time.Location {
	loc, _ := time.LoadLocation(c.Recognize.Timezone)
	return loc
}
