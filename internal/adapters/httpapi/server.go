// Package httpapi expone el servicio como una API HTTP compatible con el
// protocolo de chat completions de OpenAI: no-streaming, streaming SSE,
// listado de modelos y healthz.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ai-proxy-agent-harness/internal/adapters/upstream"
	"ai-proxy-agent-harness/internal/adapters/webui"
	"ai-proxy-agent-harness/internal/application/service"
	"ai-proxy-agent-harness/internal/config"
	"ai-proxy-agent-harness/internal/core/engine"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
	"ai-proxy-agent-harness/internal/core/session"
)

// requestIDKey es la clave de contexto del request id.
type requestIDKey struct{}

// Errores internos para controlar el flujo del streaming.
var (
	// errStopStream corta el motor porque el run quedó pausado por tool calls
	// y el stream ya se completó.
	errStopStream = errors.New("httpapi: stream terminated by tool_calls")
	// errClientGone corta el motor cuando el cliente se desconecta.
	errClientGone = errors.New("httpapi: client disconnected")
)

// Server agrupa las dependencias de la capa HTTP.
type Server struct {
	service         *service.Service
	defaultModel    string
	exposeReasoning bool
	cfg             *config.Config
	modelCache      *modelCache
	logger          *slog.Logger
}

// New construye el handler HTTP con sus rutas: la Web UI en la raíz, la API
// compatible con OpenAI en /v1/* y la configuración en /api/config.
// lister (opcional) detecta los modelos disponibles del upstream para GET
// /v1/models; si es nil, se expone solo el modelo por defecto.
func New(svc *service.Service, cfg *config.Config, lister ports.ModelLister, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		service:         svc,
		defaultModel:    cfg.UpstreamModel,
		exposeReasoning: cfg.ExposeReasoningContent,
		cfg:             cfg,
		modelCache:      newModelCache(lister),
		logger:          logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("POST /v1/chat/completions", s.chatCompletions)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.Handle("/", webui.Handler())
	return s.requestID(s.recovery(mux))
}

// requestID asigna o reutiliza un request id (header X-Request-ID) y lo
// propaga por contexto y en la respuesta, para correlacionar logs.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestIDFrom extrae el request id del contexto (vacío si no está).
func requestIDFrom(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// newRequestID genera un id aleatorio de 32 hex (sin separadores).
func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	var req openai.ChatCompletionRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid request body: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "messages is required")
		return
	}

	run, err := s.service.Prepare(&req)
	if err != nil {
		s.logger.Error("preparing run", "request_id", requestIDFrom(r.Context()), "err", err)
		s.writeError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	defer run.Release()

	if req.Stream {
		s.streamChatCompletion(w, r, run)
		return
	}
	s.nonStreamingChatCompletion(w, r, run)
}

// nonStreamingChatCompletion consume el run entero y devuelve una respuesta
// única con el contenido, el reasoning (si se expone) y los tool calls.
func (s *Server) nonStreamingChatCompletion(w http.ResponseWriter, r *http.Request, run *service.PreparedRun) {
	var reasoning, content []string
	var toolCalls []openai.ToolCall
	err := s.service.Consume(r.Context(), run, func(ev engine.Event) error {
		switch ev.Kind {
		case engine.EventReasoning:
			reasoning = append(reasoning, ev.Text)
		case engine.EventContent:
			content = append(content, ev.Text)
		case engine.EventToolCalls:
			toolCalls = ev.ToolCalls
		}
		return nil
	})
	if err != nil {
		if isUpstreamError(err) {
			s.writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		} else {
			s.logger.Error("consuming run", "request_id", requestIDFrom(r.Context()), "err", err)
			s.writeError(w, http.StatusInternalServerError, "server_error", err.Error())
		}
		return
	}

	finalContent := strings.Join(content, "")
	paused := len(toolCalls) > 0
	if err := s.service.Persist(run, paused, finalContent); err != nil {
		s.logger.Error("persisting session", "request_id", requestIDFrom(r.Context()), "err", err)
		s.writeError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	response := openai.ChatCompletionResponse{
		ID:      "chatcmpl-" + session.NewSessionID(),
		Object:  openai.ObjectChatCompletion,
		Created: time.Now().Unix(),
		Model:   run.Model,
		Choices: []openai.Choice{{
			Index: 0,
			Message: openai.ResponseMessage{
				Role:             openai.RoleAssistant,
				Content:          stringPtr(finalContent),
				ReasoningContent: s.reasoningPtr(reasoning),
				ToolCalls:        toolCalls,
			},
			FinishReason: finishReason(paused),
		}},
	}
	s.writeJSON(w, http.StatusOK, response)
}

// streamChatCompletion emite el run como SSE: rol → reasoning? → content →
// final → [DONE]; ante tool calls, persiste la pausa y emite
// tool_calls → final(tool_calls) → [DONE].
func (s *Server) streamChatCompletion(w http.ResponseWriter, r *http.Request, run *service.PreparedRun) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "server_error", "streaming not supported by the server")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	chunkID := "chatcmpl-" + session.NewSessionID()
	model := run.Model

	if err := s.writeChunk(w, flusher, roleChunk(model, chunkID)); err != nil {
		return
	}

	var content strings.Builder
	err := s.service.Consume(r.Context(), run, func(ev engine.Event) error {
		switch ev.Kind {
		case engine.EventReasoning:
			if !s.exposeReasoning {
				return nil
			}
			if err := s.writeChunk(w, flusher, reasoningChunk(model, chunkID, ev.Text)); err != nil {
				return errClientGone
			}
		case engine.EventContent:
			content.WriteString(ev.Text)
			if err := s.writeChunk(w, flusher, contentChunk(model, chunkID, ev.Text)); err != nil {
				return errClientGone
			}
		case engine.EventToolCalls:
			if err := s.service.Persist(run, true, content.String()); err != nil {
				return fmt.Errorf("persisting paused session: %w", err)
			}
			if err := s.writeChunk(w, flusher, toolCallsChunk(model, chunkID, ev.ToolCalls)); err != nil {
				return errClientGone
			}
			if err := s.writeChunk(w, flusher, finalChunk(model, chunkID, "tool_calls")); err != nil {
				return errClientGone
			}
			if err := s.writeDone(w, flusher); err != nil {
				return errClientGone
			}
			return errStopStream
		}
		return nil
	})

	switch {
	case errors.Is(err, errStopStream):
		return
	case errors.Is(err, errClientGone):
		s.logger.Info("client disconnected during streaming", "request_id", requestIDFrom(r.Context()))
		return
	case err != nil:
		s.logger.Error("consuming stream", "request_id", requestIDFrom(r.Context()), "err", err)
		_ = s.writeErrorLine(w, flusher, err.Error())
		_ = s.writeDone(w, flusher)
		return
	}

	if err := s.service.Persist(run, false, content.String()); err != nil {
		s.logger.Error("persisting session after stream", "request_id", requestIDFrom(r.Context()), "err", err)
		_ = s.writeErrorLine(w, flusher, err.Error())
		_ = s.writeDone(w, flusher)
		return
	}
	_ = s.writeChunk(w, flusher, finalChunk(model, chunkID, "stop"))
	_ = s.writeDone(w, flusher)
}

// reasoningPtr devuelve el reasoning acumulado si está configurado exponerlo;
// si no, nil.
func (s *Server) reasoningPtr(parts []string) *string {
	if !s.exposeReasoning || len(parts) == 0 {
		return nil
	}
	return stringPtr(strings.Join(parts, ""))
}

// ---------------------------------------------------------------------------
// Helpers de serialización
// ---------------------------------------------------------------------------

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		s.logger.Warn("writing json response", "err", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, errorType, message string) {
	s.writeJSON(w, status, openai.ErrorResponse{
		Error: openai.ErrorDetail{Message: message, Type: errorType},
	})
}

func (s *Server) writeChunk(w http.ResponseWriter, flusher http.Flusher, chunk openai.ChatCompletionChunk) error {
	raw, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("marshaling sse chunk: %w", err)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) writeErrorLine(w http.ResponseWriter, flusher http.Flusher, message string) error {
	raw, err := json.Marshal(openai.ErrorResponse{
		Error: openai.ErrorDetail{Message: message, Type: "upstream_error"},
	})
	if err != nil {
		return err
	}
	return s.writeRawSSE(w, flusher, "data: "+string(raw)+"\n\n")
}

func (s *Server) writeDone(w http.ResponseWriter, flusher http.Flusher) error {
	return s.writeRawSSE(w, flusher, "data: [DONE]\n\n")
}

func (s *Server) writeRawSSE(w http.ResponseWriter, flusher http.Flusher, raw string) error {
	if _, err := io.WriteString(w, raw); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// recovery convierte cualquier panic en un 500 en vez de tumbar el servidor.
func (s *Server) recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("panic recovered", "request_id", requestIDFrom(r.Context()), "err", rec, "path", r.URL.Path)
				s.writeError(w, http.StatusInternalServerError, "server_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Builders de chunks SSE
// ---------------------------------------------------------------------------

func roleChunk(model, id string) openai.ChatCompletionChunk {
	role := openai.RoleAssistant
	chunk := openai.NewChatCompletionChunk(id, model)
	chunk.Choices = []openai.ChunkChoice{{Index: 0, Delta: openai.Delta{Role: &role}}}
	return *chunk
}

func reasoningChunk(model, id, text string) openai.ChatCompletionChunk {
	chunk := openai.NewChatCompletionChunk(id, model)
	chunk.Choices = []openai.ChunkChoice{{Index: 0, Delta: openai.Delta{ReasoningContent: &text}}}
	return *chunk
}

func contentChunk(model, id, text string) openai.ChatCompletionChunk {
	chunk := openai.NewChatCompletionChunk(id, model)
	chunk.Choices = []openai.ChunkChoice{{Index: 0, Delta: openai.Delta{Content: &text}}}
	return *chunk
}

func toolCallsChunk(model, id string, toolCalls []openai.ToolCall) openai.ChatCompletionChunk {
	chunk := openai.NewChatCompletionChunk(id, model)
	chunk.Choices = []openai.ChunkChoice{{
		Index: 0,
		Delta: openai.Delta{ToolCalls: openai.ToToolCallDeltas(toolCalls)},
	}}
	return *chunk
}

func finalChunk(model, id, finishReason string) openai.ChatCompletionChunk {
	chunk := openai.NewChatCompletionChunk(id, model)
	chunk.Choices = []openai.ChunkChoice{{Index: 0, FinishReason: &finishReason}}
	return *chunk
}

func isUpstreamError(err error) bool {
	var upErr *upstream.Error
	return errors.As(err, &upErr)
}

func finishReason(paused bool) *string {
	if paused {
		return stringPtr("tool_calls")
	}
	return stringPtr("stop")
}

func stringPtr(value string) *string {
	return &value
}
