package config

import (
	"testing"
	"time"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"UPSTREAM_BASE_URL", "UPSTREAM_API_KEY", "DEEPSEEK_API_KEY", "UPSTREAM_MODEL",
		"MAX_DECOMPOSITION_DEPTH", "MAX_TOOL_ROUNDS_PER_PHASE", "PROXY_HOST", "PROXY_PORT",
		"REQUEST_TIMEOUT_SECONDS", "SESSION_TTL_SECONDS", "MAX_SESSIONS",
		"EXPOSE_REASONING_CONTENT", "SESSIONS_DIR", "LOG_LEVEL",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.UpstreamBaseURL != "https://api.deepseek.com" {
		t.Errorf("unexpected upstream base url: %q", cfg.UpstreamBaseURL)
	}
	if cfg.MaxDecompositionDepth != 3 {
		t.Errorf("unexpected max depth: %d", cfg.MaxDecompositionDepth)
	}
	if cfg.ProxyPort != 8000 {
		t.Errorf("unexpected port: %d", cfg.ProxyPort)
	}
	if cfg.RequestTimeout != 120*time.Second {
		t.Errorf("unexpected timeout: %v", cfg.RequestTimeout)
	}
	if !cfg.ExposeReasoningContent {
		t.Errorf("reasoning should be exposed by default")
	}
	if cfg.LogLevel.String() != "INFO" {
		t.Errorf("unexpected log level: %v", cfg.LogLevel)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("UPSTREAM_BASE_URL", "http://localhost:11434")
	t.Setenv("UPSTREAM_MODEL", "llama3.1")
	t.Setenv("MAX_DECOMPOSITION_DEPTH", "5")
	t.Setenv("PROXY_PORT", "9000")
	t.Setenv("SESSION_TTL_SECONDS", "10m")
	t.Setenv("EXPOSE_REASONING_CONTENT", "false")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.UpstreamBaseURL != "http://localhost:11434" {
		t.Errorf("unexpected url: %q", cfg.UpstreamBaseURL)
	}
	if cfg.MaxDecompositionDepth != 5 {
		t.Errorf("unexpected depth: %d", cfg.MaxDecompositionDepth)
	}
	if cfg.ProxyPort != 9000 {
		t.Errorf("unexpected port: %d", cfg.ProxyPort)
	}
	if cfg.SessionTTL != 10*time.Minute {
		t.Errorf("unexpected ttl: %v", cfg.SessionTTL)
	}
	if cfg.ExposeReasoningContent {
		t.Errorf("reasoning should be hidden")
	}
	if cfg.LogLevel.String() != "DEBUG" {
		t.Errorf("unexpected log level: %v", cfg.LogLevel)
	}
	if got := cfg.Addr(); got != "127.0.0.1:9000" {
		t.Errorf("unexpected addr: %q", got)
	}
}

func TestLoadAPIKeyFallback(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEEPSEEK_API_KEY", "sk-fallback")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.UpstreamAPIKey != "sk-fallback" {
		t.Errorf("expected fallback key, got %q", cfg.UpstreamAPIKey)
	}

	t.Setenv("UPSTREAM_API_KEY", "sk-primary")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.UpstreamAPIKey != "sk-primary" {
		t.Errorf("expected primary key to win, got %q", cfg.UpstreamAPIKey)
	}
}

func TestLoadInvalidInt(t *testing.T) {
	clearEnv(t)
	t.Setenv("MAX_DECOMPOSITION_DEPTH", "abc")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for invalid integer")
	}
}

func TestLoadInvalidURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("UPSTREAM_BASE_URL", "not a url")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for invalid url")
	}
}

func TestLoadInvalidPort(t *testing.T) {
	clearEnv(t)
	t.Setenv("PROXY_PORT", "70000")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for out-of-range port")
	}
}

func TestLoadInvalidBool(t *testing.T) {
	clearEnv(t)
	t.Setenv("EXPOSE_REASONING_CONTENT", "maybe")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for invalid boolean")
	}
}

func TestLoadInvalidLogLevel(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOG_LEVEL", "trace")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for invalid log level")
	}
}
