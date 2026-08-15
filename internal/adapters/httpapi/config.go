package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"ai-proxy-agent-harness/internal/config"
)

// getConfig devuelve la configuración resuelta para la Web UI. La API key
// nunca se expone: solo se informa si está seteada.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"config":    s.cfg.Values(),
		"apiKeySet": s.cfg.UpstreamAPIKey != "",
	})
}

// putConfig valida y persiste un override parcial de configuración en el
// archivo .env. Los cambios aplican al reiniciar el proxy. Una API key vacía
// se interpreta como "conservar la actual" (no se sobrescribe ni se blanquea).
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config map[string]string `json:"config"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid request body: "+err.Error())
		return
	}
	if len(body.Config) == 0 {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "config is required")
		return
	}

	if v, ok := body.Config["UPSTREAM_API_KEY"]; ok && strings.TrimSpace(v) == "" {
		delete(body.Config, "UPSTREAM_API_KEY")
	}

	if err := s.cfg.ValidateOverride(body.Config); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if err := config.WriteEnvFile(config.EnvFile, body.Config); err != nil {
		s.logger.Error("writing config file", "request_id", requestIDFrom(r.Context()), "err", err)
		s.writeError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "saved",
		"message": "Configuración guardada en .env. Reinicia el proxy para aplicar los cambios.",
	})
}
