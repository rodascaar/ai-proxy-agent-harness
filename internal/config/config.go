// Package config centraliza la configuración de la aplicación.
//
// La configuración se lee de variables de entorno (12-factor), con soporte
// opcional de un archivo .env (ignorado si no existe). Cualquier valor
// malformado o invariante roto produce un error fail-fast en Load().
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
const (
	defaultUpstreamBaseURL       = "https://api.deepseek.com"
	defaultUpstreamModel         = "deepseek-v4-flash"
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
	SessionsDir            string
	LogLevel               slog.Level
}

// Addr devuelve la dirección host:puerto donde debe escuchar el proxy.
func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.ProxyHost, c.ProxyPort)
}

// Load resuelve la configuración desde el entorno. Es fail-fast: un valor
// malformado o un invariante inválido aborta con un error contextualizado.
func Load() (*Config, error) {
	if err := loadDotEnvIfPresent(); err != nil {
		return nil, err
	}

	maxDepth, err := envInt("MAX_DECOMPOSITION_DEPTH", defaultMaxDecompositionDepth)
	if err != nil {
		return nil, err
	}
	maxToolRounds, err := envInt("MAX_TOOL_ROUNDS_PER_PHASE", defaultMaxToolRoundsPerPhase)
	if err != nil {
		return nil, err
	}
	port, err := envInt("PROXY_PORT", defaultProxyPort)
	if err != nil {
		return nil, err
	}
	timeout, err := envDuration("REQUEST_TIMEOUT_SECONDS", defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	ttl, err := envDuration("SESSION_TTL_SECONDS", defaultSessionTTL)
	if err != nil {
		return nil, err
	}
	maxSessions, err := envInt("MAX_SESSIONS", defaultMaxSessions)
	if err != nil {
		return nil, err
	}
	exposeReasoning, err := envBool("EXPOSE_REASONING_CONTENT", true)
	if err != nil {
		return nil, err
	}
	logLevel, err := envLogLevel("LOG_LEVEL", defaultLogLevel)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		UpstreamBaseURL:        envString("UPSTREAM_BASE_URL", defaultUpstreamBaseURL),
		UpstreamAPIKey:         upstreamAPIKey(),
		UpstreamModel:          envString("UPSTREAM_MODEL", defaultUpstreamModel),
		MaxDecompositionDepth:  maxDepth,
		MaxToolRoundsPerPhase:  maxToolRounds,
		ProxyHost:              envString("PROXY_HOST", defaultProxyHost),
		ProxyPort:              port,
		RequestTimeout:         timeout,
		SessionTTL:             ttl,
		MaxSessions:            maxSessions,
		ExposeReasoningContent: exposeReasoning,
		SessionsDir:            envString("SESSIONS_DIR", defaultSessionsDir),
		LogLevel:               logLevel,
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
	parsed, err := url.Parse(c.UpstreamBaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("invalid upstream base url %q: must be an absolute http(s) url", c.UpstreamBaseURL)
	}
	if c.UpstreamModel == "" {
		return errors.New("invalid config: upstream model must not be empty")
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

func envString(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: must be an integer", key, raw)
	}
	return value, nil
}

func envBool(key string, fallback bool) (bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s=%q: must be a boolean", key, raw)
	}
	return value, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: must be a duration like 120s or 2m", key, raw)
	}
	return value, nil
}

func envLogLevel(key, fallback string) (slog.Level, error) {
	raw, ok := os.LookupEnv(key)
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

// upstreamAPIKey prefiere UPSTREAM_API_KEY y cae a DEEPSEEK_API_KEY como
// conveniencia.
func upstreamAPIKey() string {
	if value, ok := os.LookupEnv("UPSTREAM_API_KEY"); ok && value != "" {
		return value
	}
	return os.Getenv("DEEPSEEK_API_KEY")
}
