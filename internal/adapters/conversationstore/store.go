// Package conversationstore persiste las conversaciones del chat de la Web UI
// como archivos JSON (uno por conversación) en un directorio. Es el "historial
// de chat" del usuario: independiente del store de sesiones del motor (que
// sigue siendo interno, indexado por hash-chain para pausa/reanudación).
//
// Una conversación se crea de forma perezosa al primer mensaje y acumula el
// transcript (mensajes user/assistant) que la UI muestra y puede reabrir.
package conversationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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

// Store persiste conversaciones como JSON con write-through atómico.
type Store struct {
	dir    string
	logger *slog.Logger

	mu    sync.Mutex
	byID  map[string]*Conversation
	locks map[string]*sync.Mutex
}

// New abre (o crea) el directorio y carga las conversaciones existentes,
// recuperando el historial tras un reinicio. Los archivos corruptos se ignoran
// con un warning sin tumbar el arranque.
func New(dir string, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating conversations dir: %w", err)
	}
	s := &Store{
		dir:    dir,
		logger: logger,
		byID:   map[string]*Conversation{},
		locks:  map[string]*sync.Mutex{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// List devuelve los resúmenes de todas las conversaciones, ordenadas por
// última actividad (más reciente primero).
func (s *Store) List(ctx context.Context) []Summary {
	s.mu.Lock()
	all := make([]*Conversation, 0, len(s.byID))
	for _, conv := range s.byID {
		all = append(all, conv)
	}
	s.mu.Unlock()

	sort.Slice(all, func(i, j int) bool {
		return all[i].UpdatedAt.After(all[j].UpdatedAt)
	})
	summaries := make([]Summary, 0, len(all))
	for _, conv := range all {
		summaries = append(summaries, Summary{
			ID:            conv.ID,
			Title:         conv.Title,
			Model:         conv.Model,
			UpdatedAt:     conv.UpdatedAt,
			MessagesCount: len(conv.Messages),
		})
	}
	return summaries
}

// Get devuelve una conversación completa. Devuelve ErrNotFound si no existe.
func (s *Store) Get(ctx context.Context, id string) (*Conversation, error) {
	s.mu.Lock()
	conv, ok := s.byID[id]
	s.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	copied := *conv
	copied.Messages = append([]openai.Message(nil), conv.Messages...)
	return &copied, nil
}

// Append agrega mensajes al final de la conversación. Si no existe, la crea de
// forma perezosa con un título derivado del primer mensaje user. Devuelve la
// conversación actualizada.
func (s *Store) Append(ctx context.Context, id, model string, messages []openai.Message) (*Conversation, error) {
	if !ValidateID(id) {
		return nil, fmt.Errorf("invalid conversation id %q", id)
	}

	lock := s.Lock(id)
	lock.Lock()
	defer lock.Unlock()

	conv, ok := s.byID[id]
	now := time.Now()
	if !ok {
		conv = &Conversation{
			ID:        id,
			CreatedAt: now,
			Messages:  make([]openai.Message, 0, len(messages)),
		}
		s.byID[id] = conv
	}
	if conv.Title == "" {
		conv.Title = deriveTitle(messages)
	}
	if model != "" {
		conv.Model = model
	}
	conv.Messages = append(conv.Messages, messages...)
	conv.UpdatedAt = now
	if err := s.writeFile(conv); err != nil {
		return nil, err
	}
	return conv, nil
}

// Rename actualiza el título de una conversación.
func (s *Store) Rename(ctx context.Context, id, title string) (*Conversation, error) {
	lock := s.Lock(id)
	lock.Lock()
	defer lock.Unlock()

	conv, ok := s.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	conv.Title = strings.TrimSpace(title)
	if conv.Title == "" {
		conv.Title = "(sin título)"
	}
	conv.UpdatedAt = time.Now()
	if err := s.writeFile(conv); err != nil {
		return nil, err
	}
	return conv, nil
}

// Delete elimina una conversación del disco y de la memoria.
func (s *Store) Delete(ctx context.Context, id string) error {
	if !ValidateID(id) {
		return fmt.Errorf("invalid conversation id %q", id)
	}
	s.mu.Lock()
	delete(s.byID, id)
	if lock, ok := s.locks[id]; ok && lock.TryLock() {
		lock.Unlock()
		delete(s.locks, id)
	}
	s.mu.Unlock()

	path := s.conversationPath(id)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing conversation: %w", err)
	}
	return nil
}

// Lock devuelve el mutex por-conversación para serializar escrituras
// concurrentes sobre la misma conversación.
func (s *Store) Lock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.locks[id]
	if !ok {
		lock = &sync.Mutex{}
		s.locks[id] = lock
	}
	return lock
}

// ValidateID valida un id de conversación: 8-64 caracteres alfanuméricos,
// guiones o guiones bajos (un UUID hex cabe; un path traversal no).
func ValidateID(id string) bool {
	return conversationIDPattern.MatchString(id)
}

// load lee todos los JSON del directorio al arrancar.
func (s *Store) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("reading conversations dir: %w", err)
	}
	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		conv, err := readFile(path)
		if err != nil {
			s.logger.Warn("skipping unreadable conversation", "file", entry.Name(), "err", err)
			continue
		}
		s.byID[conv.ID] = conv
		loaded++
	}
	s.logger.Info("loaded conversations", "count", loaded)
	return nil
}

// writeFile escribe la conversación de forma atómica (temp + rename).
func (s *Store) writeFile(conv *Conversation) error {
	raw, err := json.MarshalIndent(conv, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling conversation: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, ".conversation-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp conversation: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
		_ = tmp.Close()
	}()
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("writing temp conversation: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing temp conversation: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp conversation: %w", err)
	}
	if err := os.Rename(tmpName, s.conversationPath(conv.ID)); err != nil {
		return fmt.Errorf("renaming temp conversation: %w", err)
	}
	tmpName = ""
	return nil
}

// readFile decodifica un archivo de conversación.
func readFile(path string) (*Conversation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var conv Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("decoding conversation: %w", err)
	}
	if conv.ID == "" {
		return nil, fmt.Errorf("conversation without id")
	}
	return &conv, nil
}

// deriveTitle construye el título desde el primer mensaje user con texto real
// (una sola línea, truncada).
func deriveTitle(messages []openai.Message) string {
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

// truncateRunes recorta un texto a maxRunes caracteres, respetando caracteres
// multibyte.
func truncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 || len([]rune(text)) <= maxRunes {
		return text
	}
	return string([]rune(text)[:maxRunes]) + "…"
}

func (s *Store) conversationPath(id string) string {
	return filepath.Join(s.dir, id+".json")
}
