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
		"UPSTREAM_1_BASE_URL", "UPSTREAM_1_API_KEY", "UPSTREAM_1_MODELS",
		"UPSTREAM_2_BASE_URL", "UPSTREAM_2_API_KEY", "UPSTREAM_2_MODELS",
		"UPSTREAM_3_BASE_URL", "UPSTREAM_3_API_KEY", "UPSTREAM_3_MODELS",
		"DEBATE_ENABLED", "DEBATE_ROUNDS",
		"MAX_DECOMPOSITION_DEPTH", "MAX_TOOL_ROUNDS_PER_PHASE", "PROXY_HOST", "PROXY_PORT",
		"REQUEST_TIMEOUT_SECONDS", "SESSION_TTL_SECONDS", "MAX_SESSIONS",
		"EXPOSE_REASONING_CONTENT", "WARMUP_ON_START", "SESSIONS_DIR", "LOG_LEVEL",
		"TEMPERATURE",
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

func TestLoadTemperatureDefault(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Temperature != defaultTemperature {
		t.Errorf("expected default temperature %v, got %v", defaultTemperature, cfg.Temperature)
	}
}

func TestLoadTemperatureOverride(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("TEMPERATURE", "0.7")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", cfg.Temperature)
	}
	if got := cfg.Values()["TEMPERATURE"]; got != "0.7" {
		t.Errorf("expected TEMPERATURE in Values(), got %q", got)
	}
}

func TestLoadInvalidTemperature(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	for _, value := range []string{"abc", "-0.1", "1.5"} {
		t.Setenv("TEMPERATURE", value)
		if _, err := Load(); err == nil {
			t.Errorf("expected error for TEMPERATURE=%q", value)
		}
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

func TestLoadMultiUpstream(t *testing.T) {
	clearEnv(t)
	t.Setenv("UPSTREAM_1_BASE_URL", "http://127.0.0.1:11434/v1")
	t.Setenv("UPSTREAM_1_MODELS", "qwen2.5:7b,llama3.2:3b")
	t.Setenv("UPSTREAM_2_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_2_MODELS", "gpt-4o-mini")
	t.Setenv("UPSTREAM_2_API_KEY", "sk-openai")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Upstreams) != 2 {
		t.Fatalf("expected 2 upstreams, got %d", len(cfg.Upstreams))
	}
	if cfg.Upstreams[0].BaseURL != "http://127.0.0.1:11434/v1" {
		t.Errorf("unexpected upstream 1 url: %q", cfg.Upstreams[0].BaseURL)
	}
	if len(cfg.Upstreams[0].Models) != 2 || cfg.Upstreams[0].Models[0] != "qwen2.5:7b" || cfg.Upstreams[0].Models[1] != "llama3.2:3b" {
		t.Errorf("unexpected upstream 1 models: %v", cfg.Upstreams[0].Models)
	}
	if cfg.Upstreams[1].APIKey != "sk-openai" {
		t.Errorf("expected upstream 2 api key, got %q", cfg.Upstreams[1].APIKey)
	}
	if got := cfg.DefaultModel(); got != "qwen2.5:7b" {
		t.Errorf("expected default model qwen2.5:7b, got %q", got)
	}
}

func TestLoadLegacyFallsBackToSingleUpstream(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Upstreams) != 1 {
		t.Fatalf("expected 1 upstream from legacy config, got %d", len(cfg.Upstreams))
	}
	if cfg.Upstreams[0].BaseURL != "http://localhost:11434/v1" {
		t.Errorf("unexpected legacy url: %q", cfg.Upstreams[0].BaseURL)
	}
	if len(cfg.Upstreams[0].Models) != 1 || cfg.Upstreams[0].Models[0] != "qwen2.5:7b" {
		t.Errorf("unexpected legacy models: %v", cfg.Upstreams[0].Models)
	}
}

func TestLoadLegacyModelListIsCSVSplit(t *testing.T) {
	// UPSTREAM_MODEL acepta varios modelos separados por coma (un solo
	// servidor, p. ej. LM Studio con dos modelos cargados).
	clearEnv(t)
	setRequired(t)
	t.Setenv("UPSTREAM_MODEL", "liquid/lfm2-1.2b,qwen/qwen3-1.7b")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	models := cfg.Upstreams[0].Models
	if len(models) != 2 || models[0] != "liquid/lfm2-1.2b" || models[1] != "qwen/qwen3-1.7b" {
		t.Errorf("expected 2 models from legacy CSV, got %v", models)
	}
	if got := cfg.DefaultModel(); got != "liquid/lfm2-1.2b" {
		t.Errorf("expected DefaultModel to be the first model, got %q", got)
	}
}

func TestLoadLegacyPlusAdditionalUpstream(t *testing.T) {
	// Legado (primario) + un upstream adicional remoto: conviven sin reescribir
	// la configuración previa.
	clearEnv(t)
	setRequired(t)
	t.Setenv("UPSTREAM_2_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_2_MODELS", "gpt-4o-mini")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Upstreams) != 2 {
		t.Fatalf("expected 2 upstreams (legacy + UPSTREAM_2), got %d", len(cfg.Upstreams))
	}
	if cfg.Upstreams[0].BaseURL != "http://localhost:11434/v1" {
		t.Errorf("expected legacy as primary, got %q", cfg.Upstreams[0].BaseURL)
	}
	if cfg.Upstreams[1].BaseURL != "https://api.openai.com/v1" {
		t.Errorf("expected UPSTREAM_2 as additional, got %q", cfg.Upstreams[1].BaseURL)
	}
}

func TestLoadIndexedPrimaryOverridesLegacy(t *testing.T) {
	// Si UPSTREAM_1 está presente, es el primario y el legado se ignora.
	clearEnv(t)
	setRequired(t)
	t.Setenv("UPSTREAM_1_BASE_URL", "http://127.0.0.1:11434/v1")
	t.Setenv("UPSTREAM_1_MODELS", "qwen2.5:7b")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Upstreams) != 1 {
		t.Fatalf("expected 1 upstream (indexed primary), got %d", len(cfg.Upstreams))
	}
	if cfg.Upstreams[0].BaseURL != "http://127.0.0.1:11434/v1" {
		t.Errorf("expected indexed primary, got %q", cfg.Upstreams[0].BaseURL)
	}
}

func TestLoadMultiUpstreamIgnoresEmptySlots(t *testing.T) {
	clearEnv(t)
	t.Setenv("UPSTREAM_1_BASE_URL", "http://127.0.0.1:11434/v1")
	t.Setenv("UPSTREAM_1_MODELS", "qwen2.5:7b")
	t.Setenv("UPSTREAM_3_BASE_URL", "http://127.0.0.1:9999/v1")
	t.Setenv("UPSTREAM_3_MODELS", "otro:modelo")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Upstreams) != 2 {
		t.Fatalf("expected 2 upstreams (slot 2 empty), got %d", len(cfg.Upstreams))
	}
}

func TestLoadUpstreamWithoutModels(t *testing.T) {
	clearEnv(t)
	t.Setenv("UPSTREAM_1_BASE_URL", "http://127.0.0.1:11434/v1")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error when upstream has no models")
	}
}

func TestLoadNoUpstream(t *testing.T) {
	clearEnv(t)
	if _, err := Load(); err == nil {
		t.Fatalf("expected error when no upstream is configured")
	}
}

func TestLoadDebateDefaults(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.DebateEnabled {
		t.Errorf("debate should be disabled by default")
	}
	if cfg.DebateRounds != 2 {
		t.Errorf("expected 2 debate rounds by default, got %d", cfg.DebateRounds)
	}
}

func TestLoadDebateOverrides(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("DEBATE_ENABLED", "true")
	t.Setenv("DEBATE_ROUNDS", "3")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.DebateEnabled {
		t.Errorf("debate should be enabled")
	}
	if cfg.DebateRounds != 3 {
		t.Errorf("expected 3 debate rounds, got %d", cfg.DebateRounds)
	}
}

func TestLoadInvalidDebateRounds(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("DEBATE_ROUNDS", "5")
	if _, err := Load(); err == nil {
		t.Fatalf("expected error for out-of-range debate rounds")
	}
}

func TestValuesIncludesDebateAndUpstreams(t *testing.T) {
	clearEnv(t)
	setRequired(t)
	t.Setenv("DEBATE_ENABLED", "true")
	t.Setenv("DEBATE_ROUNDS", "3")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	values := cfg.Values()
	if values["DEBATE_ENABLED"] != "true" {
		t.Errorf("unexpected debate enabled value: %q", values["DEBATE_ENABLED"])
	}
	if values["DEBATE_ROUNDS"] != "3" {
		t.Errorf("unexpected debate rounds value: %q", values["DEBATE_ROUNDS"])
	}
	if values["UPSTREAM_1_BASE_URL"] != "http://localhost:11434/v1" {
		t.Errorf("unexpected upstream 1 url value: %q", values["UPSTREAM_1_BASE_URL"])
	}
	if values["UPSTREAM_1_MODELS"] != "qwen2.5:7b" {
		t.Errorf("unexpected upstream 1 models value: %q", values["UPSTREAM_1_MODELS"])
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
