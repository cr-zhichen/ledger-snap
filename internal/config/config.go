// Package config loads config.toml, which environment variables can override
// key by key (see EnvPrefix). Secrets never live in the file: each secret
// field names the environment variable that holds it.
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
	Extraction Extraction `toml:"extraction"`
	OpenRouter OpenRouter `toml:"openrouter"`
	Decision   Decision   `toml:"decision"`
	TypeSafe   TypeSafe   `toml:"typesafe"`
	Recognize  Recognize  `toml:"recognize"`

	// EnvOverrides lists the keys set from environment variables.
	EnvOverrides []string `toml:"-"`
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
	// SyncWait is how long an edit of a written bill waits for the bill sync
	// before giving up.
	SyncWait Duration `toml:"sync_wait"`
}

type Extraction struct {
	Model           string `toml:"model"`
	Provider        string `toml:"provider"`
	SchemaInPrompt  bool   `toml:"schema_in_prompt"`
	ReasoningEffort string `toml:"reasoning_effort"`
	MaxTokens       int    `toml:"max_tokens"`
	ImageMaxSide    int    `toml:"image_max_side"`
	ImageMinSide    int    `toml:"image_min_side"`
	JPEGQuality     int    `toml:"jpeg_quality"`
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
	Provider   string     `toml:"provider"`
	Model      string     `toml:"model"`
	Thresholds Thresholds `toml:"thresholds"`
}

type Thresholds struct {
	Category float64 `toml:"category"`
	Asset    float64 `toml:"asset"`
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
	if err := c.applyEnv(); err != nil {
		return nil, err
	}
	if c.Decision.Provider != "openrouter" && c.Decision.Provider != "typesafe" {
		return nil, fmt.Errorf("decision.provider 只能是 openrouter 或 typesafe")
	}
	if _, err := time.LoadLocation(c.Recognize.Timezone); err != nil {
		return nil, fmt.Errorf("recognize.timezone 无效: %w", err)
	}
	if c.Extraction.Model == "" || c.Extraction.MaxTokens <= 0 || c.Extraction.ImageMaxSide <= 0 || c.Extraction.JPEGQuality < 1 || c.Extraction.JPEGQuality > 100 {
		return nil, fmt.Errorf("请填写有效的 extraction 模型、输出长度和图片设置")
	}
	if c.Qianji.SyncWait.Duration <= 0 {
		return nil, fmt.Errorf("qianji.sync_wait 必须大于 0")
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
