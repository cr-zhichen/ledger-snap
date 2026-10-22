package config

import (
	"strings"
	"testing"
	"time"
)

const repoConfig = "../../config.toml"

func TestEnvOverrides(t *testing.T) {
	t.Setenv("LEDGER_SNAP_DECISION_MODEL", "typesafe/jev-preview")
	t.Setenv("LEDGER_SNAP_DECISION_THRESHOLDS_CATEGORY", "0.55")
	t.Setenv("LEDGER_SNAP_FALLBACK_ENABLED", "false")
	t.Setenv("LEDGER_SNAP_SERVER_MAX_UPLOAD_MB", "20")
	t.Setenv("LEDGER_SNAP_RECOGNIZE_DEDUP_WINDOW", "10m")
	t.Setenv("LEDGER_SNAP_OCR_GOOGLE_LANGUAGE_HINTS", "zh, ja")
	// Empty means unset; compose and invite variables are not config keys.
	t.Setenv("LEDGER_SNAP_OCR_PROVIDER", "")
	t.Setenv("LEDGER_SNAP_IMAGE", "ghcr.io/cr-zhichen/ledger-snap:0.1.0")
	t.Setenv("LEDGER_SNAP_INVITE_CODES", "abc")

	c, err := Load(repoConfig)
	if err != nil {
		t.Fatal(err)
	}
	if c.Decision.Model != "typesafe/jev-preview" || c.Decision.Thresholds.Category != 0.55 || c.Fallback.Enabled ||
		c.Server.MaxUploadMB != 20 || c.Recognize.DedupWindow.Duration != 10*time.Minute ||
		strings.Join(c.OCR.Google.LanguageHints, "|") != "zh|ja" || c.OCR.Provider != "volcano" {
		t.Fatalf("overrides not applied: %+v", c)
	}
	if len(c.EnvOverrides) != 6 {
		t.Fatalf("EnvOverrides = %v", c.EnvOverrides)
	}
}

func TestEnvOverrideErrors(t *testing.T) {
	for name, value := range map[string]string{
		"LEDGER_SNAP_DECISION_MODLE":         "typo",
		"LEDGER_SNAP_SERVER_MAX_UPLOAD_MB":   "ten",
		"LEDGER_SNAP_RECOGNIZE_DEDUP_WINDOW": "5 minutes",
		"LEDGER_SNAP_DECISION_PROVIDER":      "other",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := Load(repoConfig); err == nil {
				t.Fatalf("%s=%s should fail", name, value)
			}
		})
	}
	t.Run("primary fallback turned off", func(t *testing.T) {
		t.Setenv("LEDGER_SNAP_FALLBACK_PRIMARY", "true")
		t.Setenv("LEDGER_SNAP_FALLBACK_ENABLED", "false")
		if _, err := Load(repoConfig); err == nil {
			t.Fatal("fallback.primary without fallback.enabled should fail")
		}
	})
}
