package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeConfigFile(t *testing.T, dir string, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeRawConfigFile(t *testing.T, dir string, raw string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadFromPath_GivenValidConfigJSON_ThenPopulatesFields(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, map[string]any{
		"integration_test_session": "abc123",
		"no_usage":                 true,
	})

	t.Setenv("CC_SESSION_NO_USAGE", "")

	cfg := LoadFromPath(path)

	if cfg.IntegrationTestSession != "abc123" {
		t.Errorf("IntegrationTestSession = %q, want abc123", cfg.IntegrationTestSession)
	}
	if !cfg.NoUsage {
		t.Error("NoUsage = false, want true (from JSON)")
	}
}

func TestLoadFromPath_GivenCCSessionNoUsageNonEmpty_ThenSetsNoUsage(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, map[string]any{"no_usage": false})

	t.Setenv("CC_SESSION_NO_USAGE", "1")

	cfg := LoadFromPath(path)

	if !cfg.NoUsage {
		t.Error("NoUsage = false, want true when CC_SESSION_NO_USAGE=1")
	}
}

// Guards the presence-based semantics: CC_SESSION_NO_USAGE="" must NOT enable NoUsage.
// A regression to Getenv (which conflates unset and empty) would make this fail.
func TestLoadFromPath_GivenCCSessionNoUsageEmpty_ThenDoesNotSetNoUsage(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, map[string]any{"no_usage": false})

	t.Setenv("CC_SESSION_NO_USAGE", "")

	cfg := LoadFromPath(path)

	if cfg.NoUsage {
		t.Error("NoUsage = true, want false when CC_SESSION_NO_USAGE is empty string")
	}
}

func TestFlexBool_GivenVariousTruthyValues_ThenAllParsedAsTrue(t *testing.T) {
	cases := []struct {
		label string
		json  string
	}{
		{"bool true", `{"no_usage": true}`},
		{"number 1", `{"no_usage": 1}`},
		{"string 1", `{"no_usage": "1"}`},
		{"string true", `{"no_usage": "true"}`},
		{"string yes", `{"no_usage": "yes"}`},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			dir := t.TempDir()
			path := writeRawConfigFile(t, dir, tc.json)
			t.Setenv("CC_SESSION_NO_USAGE", "")

			cfg := LoadFromPath(path)
			if !cfg.NoUsage {
				t.Errorf("NoUsage = false for JSON %s", tc.json)
			}
		})
	}
}

func TestFlexBool_GivenFalsyValues_ThenParsedAsFalse(t *testing.T) {
	cases := []struct {
		label string
		json  string
	}{
		{"bool false", `{"no_usage": false}`},
		{"number 0", `{"no_usage": 0}`},
		{"string 0", `{"no_usage": "0"}`},
		{"string no", `{"no_usage": "no"}`},
		{"string empty", `{"no_usage": ""}`},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			dir := t.TempDir()
			path := writeRawConfigFile(t, dir, tc.json)
			t.Setenv("CC_SESSION_NO_USAGE", "")

			cfg := LoadFromPath(path)
			if cfg.NoUsage {
				t.Errorf("NoUsage = true for JSON %s", tc.json)
			}
		})
	}
}

func TestLoadFromPath_GivenMissingConfigJSON_ThenReturnsZeroConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")

	cfg := LoadFromPath(path)

	if cfg.NoUsage {
		t.Error("NoUsage = true, want false on missing config")
	}
}

func TestGet_GivenReset_ThenReloadsConfig(t *testing.T) {
	t.Setenv("CC_SESSION_NO_USAGE", "1")
	Reset()
	cfg1 := Get()
	if !cfg1.NoUsage {
		t.Fatalf("first Get() NoUsage = false, want true")
	}

	t.Setenv("CC_SESSION_NO_USAGE", "")
	Reset()
	cfg2 := Get()
	if cfg2.NoUsage {
		t.Errorf("after Reset() NoUsage = true, want false")
	}
}
