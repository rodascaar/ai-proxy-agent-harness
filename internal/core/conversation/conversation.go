// Package conversation modela el historial de chat de la Web UI: un
// transcript persistido de mensajes user/assistant identificado por un id, con
// su título derivado y el modelo usado. Es dominio puro: los adaptadores de
// infraestructura (JSON, SQLite, etc.) lo persisten a través del puerto Store.
package conversation

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"ai-proxy-agent-harness/internal/core/openai"
)

// conversationIDPattern acota los ids de conversación que la UI genera (UUIDs
// hex con guiones) para que nunca puedan colarse como rutas relativas.
var conversationIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,64}$`)

// ErrNotFound se devuelve cuando la conversación no existe.
var ErrNotFound = errors.New("conversation not found")

// maxTitleRunes limita el título autogenerado de una conversación (derivado
// de la primera línea del primer mensaje user).
const maxTitleRunes = 60

// Conversation es el transcript persistido de un chat.
type Conversation struct {
	ID        string           `json:"id"`
	Title     string           `json:"title"`
	Model     string           `json:"model"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Messages  []openai.Message `json:"messages"`
}

// Summary es la vista compacta de una conversación para el listado de la UI.
type Summary struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Model         string    `json:"model"`
	UpdatedAt     time.Time `json:"updated_at"`
	MessagesCount int       `json:"messages_count"`
}

// Store es el puerto que persiste conversaciones. Los adaptadores lo
// implementan; el dominio y la capa HTTP dependen solo de esta interfaz.
type Store interface {
	// List devuelve los resúmenes de todas las conversaciones, ordenadas por
	// última actividad (más reciente primero).
	List(ctx context.Context) []Summary
	// Get devuelve una conversación completa. Devuelve ErrNotFound si no
	// existe.
	Get(ctx context.Context, id string) (*Conversation, error)
	// Append agrega mensajes al final de la conversación. Si no existe, la
	// crea de forma perezosa con un título derivado del primer mensaje user.
	// Devuelve la conversación actualizada.
	Append(ctx context.Context, id, model string, messages []openai.Message) (*Conversation, error)
	// Rename actualiza el título de una conversación.
	Rename(ctx context.Context, id, title string) (*Conversation, error)
	// Delete elimina una conversación y sus mensajes.
	Delete(ctx context.Context, id string) error
}

// ValidateID valida un id de conversación: 8-64 caracteres alfanuméricos,
// guiones o guiones bajos (un UUID hex cabe; un path traversal no).
func ValidateID(id string) bool {
	return conversationIDPattern.MatchString(id)
}

// DeriveTitle construye el título desde el primer mensaje user con texto real
// (una sola línea, truncada).
func DeriveTitle(messages []openai.Message) string {
	for _, message := range messages {
		if message.Role != openai.RoleUser || message.Content == nil {
			continue
		}
		firstLine := message.Content.Text
		if idx := strings.Index(firstLine, "\n"); idx > 0 {
			firstLine = firstLine[:idx]
		}
		firstLine = strings.TrimSpace(firstLine)
		if firstLine == "" {
			continue
		}
		return truncateRunes(firstLine, maxTitleRunes)
	}
	return "(sin título)"
}

// TruncateRunes recorta un texto a maxRunes caracteres, respetando caracteres
// multibyte.
func TruncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 || len([]rune(text)) <= maxRunes {
		return text
	}
	return string([]rune(text)[:maxRunes]) + "…"
}

// truncateRunes es la versión interna (alias) usada por DeriveTitle.
func truncateRunes(text string, maxRunes int) string {
	return TruncateRunes(text, maxRunes)
}
