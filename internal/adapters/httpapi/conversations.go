package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"ai-proxy-agent-harness/internal/application/service"
	"ai-proxy-agent-harness/internal/core/conversation"
	"ai-proxy-agent-harness/internal/core/openai"
)

// conversationIDHeader es el header que la Web UI usa para indicar en qué
// conversación se guarda el turno del chat.
const conversationIDHeader = "X-Conversation-ID"

// listConversations expone GET /api/conversations: los resúmenes de todas las
// conversaciones persistidas, ordenados por última actividad.
func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	if s.conversations == nil {
		s.writeError(w, http.StatusServiceUnavailable, "server_error", "conversations store not available")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"conversations": s.conversations.List(r.Context()),
	})
}

// getConversation expone GET /api/conversations/{id}: el transcript completo
// de una conversación.
func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	if s.conversations == nil {
		s.writeError(w, http.StatusServiceUnavailable, "server_error", "conversations store not available")
		return
	}
	id := r.PathValue("id")
	if !conversation.ValidateID(id) {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid conversation id")
		return
	}
	conv, err := s.conversations.Get(r.Context(), id)
	if errors.Is(err, conversation.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found_error", "conversation not found")
		return
	}
	if err != nil {
		s.logger.Error("getting conversation", "request_id", requestIDFrom(r.Context()), "err", err)
		s.writeError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}

// renameConversation expone PATCH /api/conversations/{id}: actualiza el título.
func (s *Server) renameConversation(w http.ResponseWriter, r *http.Request) {
	if s.conversations == nil {
		s.writeError(w, http.StatusServiceUnavailable, "server_error", "conversations store not available")
		return
	}
	id := r.PathValue("id")
	if !conversation.ValidateID(id) {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid conversation id")
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid request body: "+err.Error())
		return
	}
	conv, err := s.conversations.Rename(r.Context(), id, body.Title)
	if errors.Is(err, conversation.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found_error", "conversation not found")
		return
	}
	if err != nil {
		s.logger.Error("renaming conversation", "request_id", requestIDFrom(r.Context()), "err", err)
		s.writeError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}

// deleteConversation expone DELETE /api/conversations/{id}.
func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	if s.conversations == nil {
		s.writeError(w, http.StatusServiceUnavailable, "server_error", "conversations store not available")
		return
	}
	id := r.PathValue("id")
	if !conversation.ValidateID(id) {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid conversation id")
		return
	}
	err := s.conversations.Delete(r.Context(), id)
	if errors.Is(err, conversation.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "not_found_error", "conversation not found")
		return
	}
	if err != nil {
		s.logger.Error("deleting conversation", "request_id", requestIDFrom(r.Context()), "err", err)
		s.writeError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// recordUserTurn persiste el mensaje user del turno actual en la conversación
// indicada por el header X-Conversation-ID. Se llama al inicio del run para que
// el mensaje del usuario quede en el historial aunque el run falle después.
func (s *Server) recordUserTurn(r *http.Request, run *service.PreparedRun, convID string) {
	if s.conversations == nil || convID == "" || !conversation.ValidateID(convID) {
		return
	}
	userMessages := trailingUserMessages(run.Messages)
	if len(userMessages) == 0 {
		return
	}
	if _, err := s.conversations.Append(r.Context(), convID, run.Model, userMessages); err != nil {
		s.logger.Warn("recording user turn in conversation", "request_id", requestIDFrom(r.Context()), "conversation", convID, "err", err)
	}
}

// recordAssistantTurn persiste el contenido final (y el reasoning, si se
// expone) del assistant en la conversación, una vez que el run terminó (no
// pausado) con contenido.
func (s *Server) recordAssistantTurn(r *http.Request, run *service.PreparedRun, convID, finalContent, finalReasoning string) {
	if s.conversations == nil || convID == "" || !conversation.ValidateID(convID) {
		return
	}
	if finalContent == "" {
		return
	}
	message := openai.Message{Role: openai.RoleAssistant, Content: openai.NewTextContent(finalContent)}
	if reasoning := s.reasoningPtr(finalReasoning); reasoning != nil {
		message.ReasoningContent = reasoning
	}
	if _, err := s.conversations.Append(r.Context(), convID, run.Model, []openai.Message{message}); err != nil {
		s.logger.Warn("recording assistant turn in conversation", "request_id", requestIDFrom(r.Context()), "conversation", convID, "err", err)
	}
}

// trailingUserMessages devuelve los mensajes user del turno actual: los que
// quedan después del último mensaje assistant con texto real (el cierre del
// turno previo). En el uso de la Web UI es exactamente el mensaje que el
// usuario acaba de enviar; un request externo sin cierre previo devuelve todos
// los user messages.
func trailingUserMessages(messages []openai.Message) []openai.Message {
	boundary := -1
	for index, message := range messages {
		if message.Role == openai.RoleAssistant && message.Content != nil && message.Content.Text != "" {
			boundary = index
		}
	}
	var result []openai.Message
	for index, message := range messages {
		if message.Role != openai.RoleUser || message.Content == nil {
			continue
		}
		if index > boundary {
			result = append(result, message)
		}
	}
	return result
}
