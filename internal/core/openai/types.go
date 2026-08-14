// Package openai define los tipos del protocolo wire de OpenAI
// (/v1/chat/completions) que comparten el dominio, los puertos y los
// adaptadores. Son tipos puros de datos sin lógica de negocio.
package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Role es el rol de un mensaje de chat, siguiendo el protocolo OpenAI.
type Role string

// Roles soportados por el protocolo.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Objetos de respuesta del protocolo.
const (
	ObjectChatCompletion      = "chat.completion"
	ObjectChatCompletionChunk = "chat.completion.chunk"
	ObjectList                = "list"
	ObjectModel               = "model"
)

// PartType identifica los tipos de contenido multimodal soportados.
const (
	PartTypeText  = "text"
	PartTypeImage = "image_url"
)

// ImageURL referencia una imagen por URL (incluidas las data: URLs).
type ImageURL struct {
	URL string `json:"url"`
}

// ContentPart es un fragmento del arreglo multimodal de OpenAI. La parte
// original se conserva íntegra en Raw para reinyectarse sin re-encodificar
// (importante con data: URLs de gran tamaño); Type y Text se exponen para
// clasificar la parte.
type ContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *ImageURL       `json:"image_url,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

// UnmarshalJSON conserva la parte original y extrae los campos conocidos.
func (p *ContentPart) UnmarshalJSON(b []byte) error {
	p.Raw = append(json.RawMessage(nil), b...)
	var known struct {
		Type     string    `json:"type"`
		Text     string    `json:"text"`
		ImageURL *ImageURL `json:"image_url"`
	}
	if err := json.Unmarshal(b, &known); err != nil {
		return fmt.Errorf("unmarshal content part: %w", err)
	}
	p.Type = known.Type
	p.Text = known.Text
	p.ImageURL = known.ImageURL
	return nil
}

// MarshalJSON reutiliza la parte original cuando existe; si no, la compone
// desde los campos tipados.
func (p ContentPart) MarshalJSON() ([]byte, error) {
	if len(p.Raw) > 0 {
		return p.Raw, nil
	}
	return json.Marshal(struct {
		Type     string    `json:"type"`
		Text     string    `json:"text,omitempty"`
		ImageURL *ImageURL `json:"image_url,omitempty"`
	}{
		Type:     p.Type,
		Text:     p.Text,
		ImageURL: p.ImageURL,
	})
}

// NewTextPart construye una parte de texto.
func NewTextPart(text string) ContentPart {
	return ContentPart{Type: PartTypeText, Text: text}
}

// NewImagePart construye una parte de imagen desde su URL.
func NewImagePart(url string) ContentPart {
	return ContentPart{Type: PartTypeImage, ImageURL: &ImageURL{URL: url}}
}

// NewTextContent construye un Content solo de texto.
func NewTextContent(text string) *Content {
	return &Content{Text: text}
}

// Content modela el campo content de un mensaje, que el protocolo permite
// como string plano o como arreglo de ContentPart. Text acumula todo el
// texto (tanto el content string como las partes de tipo text); Parts guarda
// las partes no-texto (imágenes, etc.).
type Content struct {
	Text  string
	Parts []ContentPart
}

// UnmarshalJSON acepta null, string o arreglo de partes.
func (c *Content) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		return json.Unmarshal(trimmed, &c.Text)
	}

	var parts []ContentPart
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return fmt.Errorf("unmarshal content parts: %w", err)
	}
	var text strings.Builder
	for _, part := range parts {
		if part.Type == PartTypeText {
			text.WriteString(part.Text)
			continue
		}
		c.Parts = append(c.Parts, part)
	}
	c.Text = text.String()
	return nil
}

// MarshalJSON emite string plano cuando no hay partes extra y arreglo
// (texto + partes) cuando las hay, manteniendo compatibilidad con upstreams
// sin soporte multimodal.
func (c Content) MarshalJSON() ([]byte, error) {
	if len(c.Parts) == 0 {
		return json.Marshal(c.Text)
	}
	parts := make([]ContentPart, 0, len(c.Parts)+1)
	if c.Text != "" {
		parts = append(parts, NewTextPart(c.Text))
	}
	parts = append(parts, c.Parts...)
	return json.Marshal(parts)
}

// HasParts indica si el contenido incluye partes no-texto (imágenes).
func (c *Content) HasParts() bool {
	return c != nil && len(c.Parts) > 0
}

// FunctionCall describe una invocación de herramienta dentro de un mensaje
// assistant.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall es la forma final (acumulada) de un tool call del assistant.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// FunctionDelta agrupa los campos parciales de función de un delta.
type FunctionDelta struct {
	Name      *string `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
}

// ToolCallDelta es el fragmento incremental de un tool call en streaming.
type ToolCallDelta struct {
	Index    *int           `json:"index,omitempty"`
	ID       *string        `json:"id,omitempty"`
	Type     *string        `json:"type,omitempty"`
	Function *FunctionDelta `json:"function,omitempty"`
}

// ToToolCallDeltas convierte tool calls finales en un único chunk de deltas
// (sin índice incremental), tal como el proxy los expone en su SSE: el
// cliente recibe los tool calls completos de una vez.
func ToToolCallDeltas(toolCalls []ToolCall) []ToolCallDelta {
	deltas := make([]ToolCallDelta, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		name := toolCall.Function.Name
		arguments := toolCall.Function.Arguments
		deltas = append(deltas, ToolCallDelta{
			ID:   &toolCall.ID,
			Type: &toolCall.Type,
			Function: &FunctionDelta{
				Name:      &name,
				Arguments: &arguments,
			},
		})
	}
	return deltas
}

// FunctionDef describe una herramienta declarada en el request.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Tool es una herramienta declarada en el request, en formato OpenAI.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// Message es un mensaje del historial de chat en formato OpenAI.
type Message struct {
	Role             Role       `json:"role"`
	Content          *Content   `json:"content,omitempty"`
	ReasoningContent *string    `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       *string    `json:"tool_call_id,omitempty"`
	Name             *string    `json:"name,omitempty"`
}

// Delta es el fragmento incremental de la respuesta del assistant en
// streaming.
type Delta struct {
	Role             *Role           `json:"role,omitempty"`
	Content          *string         `json:"content,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`
}

// ToolChoiceIsNone indica si un tool_choice es el valor especial "none"
// (desactiva herramientas). Cualquier otra forma (auto, o un objeto) no lo es.
func ToolChoiceIsNone(choice json.RawMessage) bool {
	if len(bytes.TrimSpace(choice)) == 0 {
		return false
	}
	var value string
	if err := json.Unmarshal(choice, &value); err != nil {
		return false
	}
	return value == "none"
}
