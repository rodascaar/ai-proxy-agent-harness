// Package session modela la persistencia de sesiones de pausa/reanudación:
// guarda el estado del motor (árbol, resultados, fase pendiente) indexado por
// un hash encadenado del historial de mensajes, para reanudar un turno
// pausado por una tool call sin rehacer trabajo ya resuelto.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"ai-proxy-agent-harness/internal/core/engine"
	"ai-proxy-agent-harness/internal/core/goal"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/task"
)

// NewSessionID genera un id de sesión aleatorio de 32 hex.
func NewSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand nunca devuelve error (Go >= 1.24); si algún día lo hace,
		// fallamos ruidosamente en vez de guardar sesiones con ids vacíos.
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}

// State es la foto persistible del estado de un run del motor entre peticiones
// HTTP. Todo el estado es JSON-serializable para poder almacenarlo en las
// notas markdown del store.
type State struct {
	SessionID           string            `json:"session_id"`
	CheckpointHash      string            `json:"checkpoint_hash"`
	CheckpointLen       int               `json:"checkpoint_len"`
	GoalCtx             goal.Context      `json:"goal_ctx"`
	Model               string            `json:"model"`
	Tools               []openai.Tool     `json:"tools,omitempty"`
	ToolChoice          json.RawMessage   `json:"tool_choice,omitempty"`
	Root                *task.Node        `json:"root"`
	Leaves              []*task.Node      `json:"leaves"`
	Results             []string          `json:"results"`
	PendingPhase        engine.Phase      `json:"pending_phase,omitempty"`
	PendingLeafIndex    int               `json:"pending_leaf_index,omitempty"`
	PendingToolCalls    []openai.ToolCall `json:"pending_tool_calls,omitempty"`
	PendingConversation []openai.Message  `json:"pending_conversation,omitempty"`
	ToolRoundCount      int               `json:"tool_round_count,omitempty"`
	// Solo el contenido final de cada síntesis ya entregada en turnos previos
	// de esta misma conversación — nunca el reasoning interno del proxy.
	TurnHistory []string  `json:"turn_history"`
	LastUsedAt  time.Time `json:"last_used_at"`
}

// PendingToolCallIDs devuelve los ids de las tool calls que la sesión está
// esperando.
func (s *State) PendingToolCallIDs() map[string]struct{} {
	ids := make(map[string]struct{}, len(s.PendingToolCalls))
	for _, toolCall := range s.PendingToolCalls {
		if toolCall.ID != "" {
			ids[toolCall.ID] = struct{}{}
		}
	}
	return ids
}

// Store es el puerto que persiste sesiones. Los adaptadores (markdown, etc.)
// lo implementan; el dominio solo depende de esta interfaz.
type Store interface {
	// FindMatching devuelve la sesión cuyo checkpoint coincide con el prefijo
	// del historial de mensajes, o nil si no hay ninguna utilizable.
	FindMatching(ctx context.Context, messages []openai.Message) (*State, error)
	// Save persiste (o actualiza) una sesión.
	Save(ctx context.Context, state *State) error
	// Lock devuelve el mutex asociado a una sesión, para serializar
	// reanudaciones concurrentes de la misma sesión.
	Lock(sessionID string) *sync.Mutex
}

// HashChain calcula un hash acumulado por prefijo: chain[i] identifica de
// forma estable el historial messages[:i+1], para detectar cuándo una request
// nueva es continuación exacta (mismo prefijo) de una conversación ya vista.
func HashChain(messages []openai.Message) ([]string, error) {
	chain := make([]string, 0, len(messages))
	prev := ""
	for _, message := range messages {
		digest, err := messageDigest(prev, message)
		if err != nil {
			return nil, err
		}
		prev = digest
		chain = append(chain, prev)
	}
	return chain, nil
}

func messageDigest(prev string, message openai.Message) (string, error) {
	serialized, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("serializing message for hash: %w", err)
	}
	sum := sha256.Sum256([]byte(prev + "\x1e" + string(serialized)))
	return hex.EncodeToString(sum[:]), nil
}

// IsValidResume indica si `messages` extiende exactamente el checkpoint de la
// sesión con los resultados de tool que esta estaba esperando (en una hoja
// atómica o en la síntesis final — ambas quedan marcadas con pending phase).
func IsValidResume(state *State, messages []openai.Message) bool {
	if state.PendingPhase == engine.PhaseNone || len(state.PendingToolCalls) == 0 {
		return false
	}
	if len(messages) <= state.CheckpointLen {
		return false
	}
	suffix := messages[state.CheckpointLen:]
	toolIDs := make(map[string]struct{})
	for _, message := range suffix {
		if message.Role == openai.RoleTool && message.ToolCallID != nil {
			toolIDs[*message.ToolCallID] = struct{}{}
		}
	}
	for _, toolCall := range state.PendingToolCalls {
		if _, ok := toolIDs[toolCall.ID]; !ok {
			return false
		}
	}
	return true
}

// IsNewTurn indica si la sesión ya terminó su run (sin fase pendiente) pero la
// request trae mensajes nuevos más allá del checkpoint: un turno externo nuevo
// del caller sobre una conversación ya resuelta, no una reanudación de tool.
func IsNewTurn(state *State, messages []openai.Message) bool {
	return state.PendingPhase == engine.PhaseNone && len(messages) > state.CheckpointLen
}

// ExtractToolOutputs extrae los resultados de tool del sufijo de mensajes que
// resuelven las tool calls pendientes de la sesión.
func ExtractToolOutputs(state *State, messages []openai.Message) map[string]string {
	outputs := make(map[string]string)
	pending := state.PendingToolCallIDs()
	suffix := messages[state.CheckpointLen:]
	for _, message := range suffix {
		if message.Role != openai.RoleTool || message.ToolCallID == nil {
			continue
		}
		if _, ok := pending[*message.ToolCallID]; !ok {
			continue
		}
		text := ""
		if message.Content != nil {
			text = message.Content.Text
		}
		outputs[*message.ToolCallID] = text
	}
	return outputs
}
