package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"UPSTREAM_BASE_URL", "UPSTREAM_API_KEY", "UPSTREAM_MODEL",
		"MAX_DECOMPOSITION_DEPTH", "MAX_TOOL_ROUNDS_PER_PHASE", "PROXY_HOST", "PROXY_PORT",
		"REQUEST_TIMEOUT_SECONDS", "SESSION_TTL_SECONDS", "MAX_SESSIONS",
		"EXPOSE_REASONING_CONTENT", "WARMUP_ON_START", "SESSIONS_DIR", "LOG_LEVEL",
	} {
		t.Setenv(key, "")
	}
}

// setRequired fija las variables obligatorias para que Load() no falle.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("UPSTREAM_BASE_URL", "http://localhost:11434/v1")
	t.Setenv("UPSTREAM_MODEL", "qwen2.5:7b")
}

func TestLoadRequiresUpstream(t *testing.T) {
	clearEnv(t)
	if _, err := Load(); err == nil {
		t.Fatalf("expected error when UPSTREAM_BASE_URL/UPSTREAM_MODEL are missing")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
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
	if cfg.WarmupOnStart {
		t.Errorf("warmup should be disabled by default")
	}
	if cfg.LogLevel.String() != "INFO" {
		t.Errorf("unexpected log level: %v", cfg.LogLevel)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("UPSTREAM_BASE_URL", "http://localhost:11434")
	t.Setenv("UPSTREAM_MODEL", "llama3.1")
	t.Setenv("MAX_DECOMPOSITION_DEPTH", "5")
	t.Setenv("PROXY_PORT", "9000")
	t.Setenv("SESSION_TTL_SECONDS", "10m")
	t.Setenv("EXPOSE_REASONING_CONTENT", "false")
	t.Setenv("WARMUP_ON_START", "true")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.UpstreamBaseURL != "http://localhost:11434" {
		t.Errorf("unexpected url: %q", cfg.UpstreamBaseURL)
	}
	if cfg.UpstreamModel != "llama3.1" {
		t.Errorf("unexpected model: %q", cfg.UpstreamModel)
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
	if !cfg.WarmupOnStart {
		t.Errorf("warmup should be enabled")
	}
	if cfg.LogLevel.String() != "DEBUG" {
		t.Errorf("unexpected log level: %v", cfg.LogLevel)
	}
	if got := cfg.Addr(); got != "127.0.0.1:9000" {
		t.Errorf("unexpected addr: %q", got)
	}
}

func TestLoadAPIKey(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("UPSTREAM_API_KEY", "sk-local")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.UpstreamAPIKey != "sk-local" {
		t.Errorf("expected UPSTREAM_API_KEY, got %q", cfg.UpstreamAPIKey)
	}
}

func TestLoadInvalidInt(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("MAX_DECOMPOSITION_DEPTH", "abc")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for invalid integer")
	}
}

func TestLoadInvalidURL(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("UPSTREAM_BASE_URL", "not a url")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for invalid url")
	}
}

func TestLoadInvalidPort(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("PROXY_PORT", "70000")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for out-of-range port")
	}
}

func TestLoadInvalidBool(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("EXPOSE_REASONING_CONTENT", "maybe")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for invalid boolean")
	}
}

func TestLoadInvalidLogLevel(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("LOG_LEVEL", "trace")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for invalid log level")
	}
}

func TestValuesOmitsAPIKey(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("UPSTREAM_API_KEY", "sk-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	values := cfg.Values()
	if _, ok := values["UPSTREAM_API_KEY"]; ok {
		t.Errorf("Values() must not leak the API key")
	}
	if values["UPSTREAM_MODEL"] != "qwen2.5:7b" {
		t.Errorf("unexpected model value: %q", values["UPSTREAM_MODEL"])
	}
}

func TestValidateValues(t *testing.T) {
	clearEnv(t)
	setRequired(t)

	if err := ValidateValues(map[string]string{"UPSTREAM_MODEL": "otro-modelo"}); err != nil {
		t.Errorf("valid override should pass: %v", err)
	}
	if err := ValidateValues(map[string]string{"PROXY_PORT": "nope"}); err == nil {
		t.Errorf("expected error for invalid port override")
	}
	if err := ValidateValues(map[string]string{"UPSTREAM_MODEL": ""}); err == nil {
		t.Errorf("expected error when model override is empty")
	}
}

func TestWriteEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, EnvFile)

	// Crear con comentarios y claves existentes + nuevas.
	initial := "# comentario\nUPSTREAM_BASE_URL=http://old\nUPSTREAM_MODEL=modelo-viejo\nOTRA_CLAVE=x\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("writing seed: %v", err)
	}

	values := map[string]string{
		"UPSTREAM_MODEL":          "modelo-nuevo",
		"MAX_DECOMPOSITION_DEPTH": "7",
	}
	if err := WriteEnvFile(path, values); err != nil {
		t.Fatalf("WriteEnvFile() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "# comentario") {
		t.Errorf("comment lost: %q", got)
	}
	if !strings.Contains(got, "UPSTREAM_BASE_URL=http://old") {
		t.Errorf("untouched key lost: %q", got)
	}
	if !strings.Contains(got, "UPSTREAM_MODEL=modelo-nuevo") {
		t.Errorf("existing key not updated: %q", got)
	}
	if !strings.Contains(got, "OTRA_CLAVE=x") {
		t.Errorf("unknown key lost: %q", got)
	}
	if !strings.Contains(got, "MAX_DECOMPOSITION_DEPTH=7") {
		t.Errorf("new key not appended: %q", got)
	}
}
