// Package config centraliza la configuración de la aplicación.
//
// La configuración se lee de variables de entorno (12-factor), con soporte
// opcional de un archivo .env (ignorado si no existe). Cualquier valor
// malformado o invariante roto produce un error fail-fast en Load().
//
// La Web UI usa Values(), ValidateValues() y WriteEnvFile() para exponer y
// persistir la configuración sin reiniciar el proceso (los cambios aplican al
// reiniciar el proxy).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Defaults explícitos de la aplicación, para no esparcir números mágicos.
// El upstream (URL y modelo) no tiene default: es obligatorio configurarlo.
const (
	defaultMaxDecompositionDepth = 3
	defaultMaxToolRoundsPerPhase = 25
	defaultProxyHost             = "127.0.0.1"
	defaultProxyPort             = 8000
	defaultRequestTimeout        = 120 * time.Second
	defaultSessionTTL            = 30 * time.Minute
	defaultMaxSessions           = 200
	defaultSessionsDir           = ".sessions"
	defaultLogLevel              = "info"
)

// EnvFile es el nombre del archivo .env que lee el proxy y que edita la UI.
const EnvFile = ".env"

// ConfigKeys es el orden canónico de las variables editables desde la UI. Se
// usa para escribir .env de forma determinista.
var ConfigKeys = []string{
	"UPSTREAM_BASE_URL",
	"UPSTREAM_API_KEY",
	"UPSTREAM_MODEL",
	"MAX_DECOMPOSITION_DEPTH",
	"MAX_TOOL_ROUNDS_PER_PHASE",
	"PROXY_HOST",
	"PROXY_PORT",
	"REQUEST_TIMEOUT_SECONDS",
	"SESSION_TTL_SECONDS",
	"MAX_SESSIONS",
	"SESSIONS_DIR",
	"EXPOSE_REASONING_CONTENT",
	"WARMUP_ON_START",
	"LOG_LEVEL",
}

// Config agrupa toda la configuración resuelta de la aplicación.
type Config struct {
	UpstreamBaseURL        string
	UpstreamAPIKey         string
	UpstreamModel          string
	MaxDecompositionDepth  int
	MaxToolRoundsPerPhase  int
	ProxyHost              string
	ProxyPort              int
	RequestTimeout         time.Duration
	SessionTTL             time.Duration
	MaxSessions            int
	ExposeReasoningContent bool
	WarmupOnStart          bool
	SessionsDir            string
	LogLevel               slog.Level
}

// Addr devuelve la dirección host:puerto donde debe escuchar el proxy.
func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.ProxyHost, c.ProxyPort)
}

// Load resuelve la configuración desde el entorno (incluyendo .env, si
// existe). Es fail-fast: un valor malformado o un invariante inválido aborta
// con un error contextualizado.
func Load() (*Config, error) {
	if err := loadDotEnvIfPresent(); err != nil {
		return nil, err
	}
	return build(os.LookupEnv)
}

// ValidateValues valida un conjunto de variables (usado por PUT /api/config)
// sin mutar el entorno. Los valores no presentes en `values` se toman del
// entorno actual, de modo que se pueda validar un override parcial.
func ValidateValues(values map[string]string) error {
	_, err := build(func(key string) (string, bool) {
		if v, ok := values[key]; ok {
			return v, true
		}
		return os.LookupEnv(key)
	})
	return err
}

// ValidateOverride valida un override parcial sobre la configuración resuelta
// actual. Las claves no tocadas por `values` conservan su valor en c, lo que
// permite validar la configuración que resultará tras reiniciar.
func (c *Config) ValidateOverride(values map[string]string) error {
	merged := c.Values()
	for key, value := range values {
		merged[key] = value
	}
	return ValidateValues(merged)
}

// Values devuelve la configuración resuelta como mapa de variables de entorno
// para exponerla en la UI (GET /api/config). La API key NO se incluye (se
// enmascara en la capa HTTP).
func (c *Config) Values() map[string]string {
	return map[string]string{
		"UPSTREAM_BASE_URL":         c.UpstreamBaseURL,
		"UPSTREAM_MODEL":            c.UpstreamModel,
		"MAX_DECOMPOSITION_DEPTH":   strconv.Itoa(c.MaxDecompositionDepth),
		"MAX_TOOL_ROUNDS_PER_PHASE": strconv.Itoa(c.MaxToolRoundsPerPhase),
		"PROXY_HOST":                c.ProxyHost,
		"PROXY_PORT":                strconv.Itoa(c.ProxyPort),
		"REQUEST_TIMEOUT_SECONDS":   formatDuration(c.RequestTimeout),
		"SESSION_TTL_SECONDS":       formatDuration(c.SessionTTL),
		"MAX_SESSIONS":              strconv.Itoa(c.MaxSessions),
		"SESSIONS_DIR":              c.SessionsDir,
		"EXPOSE_REASONING_CONTENT":  strconv.FormatBool(c.ExposeReasoningContent),
		"WARMUP_ON_START":           strconv.FormatBool(c.WarmupOnStart),
		"LOG_LEVEL":                 strings.ToLower(c.LogLevel.String()),
	}
}

// build construye la configuración leyendo variables a través de `get`.
// Se comparte entre Load() (fuente: entorno) y ValidateValues() (fuente:
// override parcial + entorno), para no duplicar el parsing ni la validación.
func build(get func(string) (string, bool)) (*Config, error) {
	str := func(key, fallback string) string {
		if v, ok := get(key); ok && v != "" {
			return v
		}
		return fallback
	}
	num := func(key string, fallback int) (int, error) {
		raw, ok := get(key)
		if !ok || raw == "" {
			return fallback, nil
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid %s=%q: must be an integer", key, raw)
		}
		return v, nil
	}
	boolean := func(key string, fallback bool) (bool, error) {
		raw, ok := get(key)
		if !ok || raw == "" {
			return fallback, nil
		}
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return false, fmt.Errorf("invalid %s=%q: must be a boolean", key, raw)
		}
		return v, nil
	}
	duration := func(key string, fallback time.Duration) (time.Duration, error) {
		raw, ok := get(key)
		if !ok || raw == "" {
			return fallback, nil
		}
		v, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid %s=%q: must be a duration like 120s or 2m", key, raw)
		}
		return v, nil
	}
	logLevel := func(key, fallback string) (slog.Level, error) {
		raw, ok := get(key)
		if !ok || raw == "" {
			raw = fallback
		}
		switch strings.ToLower(raw) {
		case "debug":
			return slog.LevelDebug, nil
		case "info":
			return slog.LevelInfo, nil
		case "warn":
			return slog.LevelWarn, nil
		case "error":
			return slog.LevelError, nil
		default:
			return 0, fmt.Errorf("invalid %s=%q: must be debug, info, warn or error", key, raw)
		}
	}

	maxDepth, err := num("MAX_DECOMPOSITION_DEPTH", defaultMaxDecompositionDepth)
	if err != nil {
		return nil, err
	}
	maxToolRounds, err := num("MAX_TOOL_ROUNDS_PER_PHASE", defaultMaxToolRoundsPerPhase)
	if err != nil {
		return nil, err
	}
	port, err := num("PROXY_PORT", defaultProxyPort)
	if err != nil {
		return nil, err
	}
	timeout, err := duration("REQUEST_TIMEOUT_SECONDS", defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	ttl, err := duration("SESSION_TTL_SECONDS", defaultSessionTTL)
	if err != nil {
		return nil, err
	}
	maxSessions, err := num("MAX_SESSIONS", defaultMaxSessions)
	if err != nil {
		return nil, err
	}
	exposeReasoning, err := boolean("EXPOSE_REASONING_CONTENT", true)
	if err != nil {
		return nil, err
	}
	warmup, err := boolean("WARMUP_ON_START", false)
	if err != nil {
		return nil, err
	}
	level, err := logLevel("LOG_LEVEL", defaultLogLevel)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		UpstreamBaseURL:        str("UPSTREAM_BASE_URL", ""),
		UpstreamAPIKey:         str("UPSTREAM_API_KEY", ""),
		UpstreamModel:          str("UPSTREAM_MODEL", ""),
		MaxDecompositionDepth:  maxDepth,
		MaxToolRoundsPerPhase:  maxToolRounds,
		ProxyHost:              str("PROXY_HOST", defaultProxyHost),
		ProxyPort:              port,
		RequestTimeout:         timeout,
		SessionTTL:             ttl,
		MaxSessions:            maxSessions,
		ExposeReasoningContent: exposeReasoning,
		WarmupOnStart:          warmup,
		SessionsDir:            str("SESSIONS_DIR", defaultSessionsDir),
		LogLevel:               level,
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadDotEnvIfPresent carga .env si existe. Un .env malformado es un error
// real (fail-fast); que el archivo no exista es el caso normal y se ignora.
func loadDotEnvIfPresent() error {
	if err := godotenv.Load(); err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return nil
		}
		return fmt.Errorf("loading .env: %w", err)
	}
	return nil
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.UpstreamBaseURL) == "" {
		return errors.New("invalid config: UPSTREAM_BASE_URL is required (set it in .env or the Web UI)")
	}
	parsed, err := url.Parse(c.UpstreamBaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("invalid upstream base url %q: must be an absolute http(s) url", c.UpstreamBaseURL)
	}
	if strings.TrimSpace(c.UpstreamModel) == "" {
		return errors.New("invalid config: UPSTREAM_MODEL is required (set it in .env or the Web UI)")
	}
	if c.MaxDecompositionDepth < 1 {
		return errors.New("invalid config: max decomposition depth must be >= 1")
	}
	if c.MaxToolRoundsPerPhase < 1 {
		return errors.New("invalid config: max tool rounds per phase must be >= 1")
	}
	if c.ProxyPort < 1 || c.ProxyPort > 65535 {
		return fmt.Errorf("invalid config: proxy port %d out of range", c.ProxyPort)
	}
	if c.RequestTimeout <= 0 {
		return errors.New("invalid config: request timeout must be > 0")
	}
	if c.SessionTTL <= 0 {
		return errors.New("invalid config: session ttl must be > 0")
	}
	if c.MaxSessions < 1 {
		return errors.New("invalid config: max sessions must be >= 1")
	}
	if strings.TrimSpace(c.SessionsDir) == "" {
		return errors.New("invalid config: sessions dir must not be empty")
	}
	return nil
}

// WriteEnvFile actualiza (o agrega) las variables dadas en el archivo .env,
// preservando comentarios, líneas en blanco y claves no gestionadas. Crea el
// archivo si no existe. Los valores vacíos se escriben tal cual (para poder
// blanquear una variable).
func WriteEnvFile(path string, values map[string]string) error {
	lines, err := readLines(path)
	if err != nil {
		return err
	}

	written := make(map[string]bool, len(values))
	for i, line := range lines {
		key := parseKey(line)
		if key == "" {
			continue
		}
		if v, ok := values[key]; ok {
			lines[i] = key + "=" + v
			written[key] = true
		}
	}

	var extra []string
	for _, key := range ConfigKeys {
		if v, ok := values[key]; ok && !written[key] {
			extra = append(extra, key+"="+v)
			written[key] = true
		}
	}
	for key, v := range values {
		if !written[key] {
			extra = append(extra, key+"="+v)
		}
	}

	content := strings.Join(lines, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if len(extra) > 0 {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += strings.Join(extra, "\n") + "\n"
	}

	return os.WriteFile(path, []byte(content), 0o600)
}

// readLines lee el archivo en líneas, tolerando su ausencia (devuelve nil).
func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	content := strings.TrimSuffix(string(data), "\n")
	if content == "" {
		return nil, nil
	}
	return strings.Split(content, "\n"), nil
}

// parseKey extrae la clave de una línea KEY=VALUE; devuelve "" si la línea es
// un comentario o no es una asignación.
func parseKey(line string) string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	if idx := strings.Index(trimmed, "="); idx > 0 {
		return strings.TrimSpace(trimmed[:idx])
	}
	return ""
}

// formatDuration devuelve una representación compacta y amigable de la
// duración (ej. "120s", "2m", "1h") que time.ParseDuration vuelve a aceptar.
func formatDuration(d time.Duration) string {
	if d%time.Second == 0 {
		sec := int64(d / time.Second)
		switch {
		case sec%3600 == 0:
			return strconv.FormatInt(sec/3600, 10) + "h"
		case sec%60 == 0:
			return strconv.FormatInt(sec/60, 10) + "m"
		default:
			return strconv.FormatInt(sec, 10) + "s"
		}
	}
	return d.String()
}
