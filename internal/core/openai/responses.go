package openai

import (
	"encoding/json"
	"time"
)

// ChatCompletionRequest es el cuerpo de POST /v1/chat/completions. Los
// campos no usados por el proxy (temperature, max_tokens, parallel_tool_calls)
// se aceptan por compatibilidad de protocolo pero no se propagan a las
// llamadas internas, replicando el comportamiento del proxy original.
type ChatCompletionRequest struct {
	Model             *string         `json:"model,omitempty"`
	Messages          []Message       `json:"messages"`
	Stream            bool            `json:"stream,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	MaxTokens         *int            `json:"max_tokens,omitempty"`
	Tools             []Tool          `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
}

// ResolvedModel devuelve el modelo del request o el fallback si viene vacío.
func (r *ChatCompletionRequest) ResolvedModel(fallback string) string {
	if r.Model != nil && *r.Model != "" {
		return *r.Model
	}
	return fallback
}

// ResponseMessage es el mensaje de la respuesta final (no streaming).
type ResponseMessage struct {
	Role             Role       `json:"role"`
	Content          *string    `json:"content,omitempty"`
	ReasoningContent *string    `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// Choice agrupa el mensaje de una respuesta no streaming.
type Choice struct {
	Index        int             `json:"index"`
	Message      ResponseMessage `json:"message"`
	FinishReason *string         `json:"finish_reason,omitempty"`
}

// ChatCompletionResponse es la respuesta no streaming.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

// ChunkChoice agrupa el delta de un chunk de streaming.
type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        Delta   `json:"delta"`
	FinishReason *string `json:"finish_reason,omitempty"`
}

// ChatCompletionChunk es un fragmento de la respuesta streaming SSE.
type ChatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
}

// NewChatCompletionChunk construye un chunk con id único y timestamp actual.
func NewChatCompletionChunk(chunkID, model string) *ChatCompletionChunk {
	return &ChatCompletionChunk{
		ID:      chunkID,
		Object:  ObjectChatCompletionChunk,
		Created: time.Now().Unix(),
		Model:   model,
	}
}

// ModelDescriptor describe un modelo para GET /v1/models.
type ModelDescriptor struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// ErrorDetail es el cuerpo de error en formato OpenAI.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// ErrorResponse es la envoltura estándar de errores de la API.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}
