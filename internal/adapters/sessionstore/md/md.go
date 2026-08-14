// Package md implementa el puerto session.Store guardando cada sesión como una
// nota markdown en un directorio: un resumen legible del árbol y resultados,
// más un bloque JSON como fuente de verdad canónica del estado.
package md

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-proxy-agent-harness/internal/core/engine"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/session"
	"ai-proxy-agent-harness/internal/core/task"
)

// stateFencePrefix abre el bloque JSON canónico dentro de cada nota.
const stateFencePrefix = "```json"

// Store implementa session.Store sobre notas markdown con write-through
// atómico (temp + rename) y evicción por TTL / max_sessions.
type Store struct {
	dir         string
	ttl         time.Duration
	maxSessions int
	logger      *slog.Logger

	mu    sync.Mutex
	byID  map[string]*session.State
	locks map[string]*sync.Mutex
}

// New abre (o crea) el directorio de sesiones y carga las notas existentes,
// recuperando así el estado tras un reinicio. Las notas corruptas se ignoran
// con un warning, sin tumbar el arranque.
func New(dir string, ttl time.Duration, maxSessions int, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating sessions dir: %w", err)
	}
	s := &Store{
		dir:         dir,
		ttl:         ttl,
		maxSessions: maxSessions,
		logger:      logger,
		byID:        map[string]*session.State{},
		locks:       map[string]*sync.Mutex{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// FindMatching devuelve la sesión con el checkpoint de mayor longitud que
// coincida con un prefijo del historial de mensajes, o nil si no hay ninguna.
func (s *Store) FindMatching(ctx context.Context, messages []openai.Message) (*session.State, error) {
	s.mu.Lock()
	s.evictExpiredLocked()
	candidates := make([]*session.State, 0, len(s.byID))
	for _, state := range s.byID {
		candidates = append(candidates, state)
	}
	s.mu.Unlock()

	if len(candidates) == 0 || len(messages) == 0 {
		return nil, nil
	}

	chain, err := session.HashChain(messages)
	if err != nil {
		return nil, err
	}
	var best *session.State
	for _, state := range candidates {
		if state.CheckpointLen <= 0 || state.CheckpointLen > len(chain) {
			continue
		}
		if chain[state.CheckpointLen-1] != state.CheckpointHash {
			continue
		}
		if best == nil || state.CheckpointLen > best.CheckpointLen {
			best = state
		}
	}
	return best, nil
}

// Save persiste la sesión (write-through atómico) y actualiza el mapa en
// memoria, aplicando evicción por TTL y por max_sessions.
func (s *Store) Save(ctx context.Context, state *session.State) error {
	if state.SessionID == "" {
		return fmt.Errorf("cannot save session without id")
	}
	state.LastUsedAt = time.Now()
	if err := s.writeFile(state); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[state.SessionID] = state
	s.evictExpiredLocked()

	overflow := len(s.byID) - s.maxSessions
	if overflow > 0 {
		for _, stale := range s.sortedByLastUsed()[:overflow] {
			s.removeLocked(stale.SessionID)
		}
	}
	return nil
}

// Lock devuelve el mutex por-sesión para serializar reanudaciones
// concurrentes de la misma sesión.
func (s *Store) Lock(sessionID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.locks[sessionID]
	if !ok {
		lock = &sync.Mutex{}
		s.locks[sessionID] = lock
	}
	return lock
}

// load lee todas las notas .md del directorio al arrancar.
func (s *Store) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("reading sessions dir: %w", err)
	}
	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		state, err := readFile(path)
		if err != nil {
			s.logger.Warn("skipping unreadable session note", "file", entry.Name(), "err", err)
			continue
		}
		s.byID[state.SessionID] = state
		loaded++
	}
	s.logger.Info("loaded session notes", "count", loaded)
	return nil
}

// writeFile escribe la nota de forma atómica: archivo temporal + rename.
func (s *Store) writeFile(state *session.State) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling session state: %w", err)
	}
	content := renderNote(state, raw)

	tmp, err := os.CreateTemp(s.dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp note: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
		_ = tmp.Close()
	}()
	if _, err := tmp.WriteString(content); err != nil {
		return fmt.Errorf("writing temp note: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing temp note: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp note: %w", err)
	}
	if err := os.Rename(tmpName, s.sessionPath(state.SessionID)); err != nil {
		return fmt.Errorf("renaming temp note: %w", err)
	}
	tmpName = ""
	return nil
}

// renderNote compone el markdown legible + el bloque JSON canónico.
func renderNote(state *session.State, rawJSON []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Sesión %s\n\n", state.SessionID)
	fmt.Fprintf(&b, "- **Modelo**: `%s`\n", state.Model)
	fmt.Fprintf(&b, "- **Último uso**: %s\n", state.LastUsedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- **Checkpoint**: `%s` (%d mensajes)\n", state.CheckpointHash, state.CheckpointLen)
	if state.PendingPhase != engine.PhaseNone {
		fmt.Fprintf(&b, "- **Fase pendiente**: %s\n", state.PendingPhase.String())
	} else {
		b.WriteString("- **Fase pendiente**: ninguna\n")
	}
	fmt.Fprintf(&b, "- **Turnos previos**: %d\n\n", len(state.TurnHistory))

	if state.Root != nil {
		b.WriteString("## Árbol de tareas\n\n```\n")
		b.WriteString(task.RenderTree(state.Root))
		b.WriteString("\n```\n\n")
	}
	if len(state.Results) > 0 {
		b.WriteString("## Resultados\n\n")
		for i, result := range state.Results {
			fmt.Fprintf(&b, "%d. %s\n", i+1, result)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Estado (fuente de verdad)\n\n")
	b.WriteString(stateFencePrefix)
	b.WriteString("\n")
	b.Write(rawJSON)
	b.WriteString("\n```\n")
	return b.String()
}

// readFile extrae y decodifica el bloque JSON canónico de una nota.
func readFile(path string) (*session.State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, ok := extractJSONFence(string(data))
	if !ok {
		return nil, fmt.Errorf("note without valid %s fence", stateFencePrefix)
	}
	var state session.State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("decoding session state: %w", err)
	}
	return &state, nil
}

// extractJSONFence devuelve el contenido del ÚLTIMO fence ```json bien
// formado, que es la fuente de verdad canónica. Devuelve ok=false si no hay
// ninguno o si algún fence queda abierto.
func extractJSONFence(content string) ([]byte, bool) {
	lines := strings.Split(content, "\n")
	inside := false
	var buf []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inside {
			if trimmed == stateFencePrefix {
				inside = true
				buf = buf[:0]
			}
			continue
		}
		if trimmed == "```" {
			inside = false
			continue
		}
		buf = append(buf, line)
	}
	if inside || len(buf) == 0 {
		return nil, false
	}
	return []byte(strings.Join(buf, "\n")), true
}

// evictExpiredLocked borra sesiones cuyo último uso superó el TTL. Requiere
// s.mu. Un ttl <= 0 desactiva la expiración.
func (s *Store) evictExpiredLocked() {
	if s.ttl <= 0 {
		return
	}
	cutoff := time.Now().Add(-s.ttl)
	for id, state := range s.byID {
		if state.LastUsedAt.Before(cutoff) {
			s.removeLocked(id)
		}
	}
}

// sortedByLastUsed devuelve las sesiones ordenadas por último uso (ascendente).
// Requiere s.mu.
func (s *Store) sortedByLastUsed() []*session.State {
	all := make([]*session.State, 0, len(s.byID))
	for _, state := range s.byID {
		all = append(all, state)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].LastUsedAt.Before(all[j].LastUsedAt)
	})
	return all
}

// removeLocked elimina una sesión del mapa y de disco, y su lock solo si no
// está en uso por una reanudación en curso. Requiere s.mu.
func (s *Store) removeLocked(id string) {
	delete(s.byID, id)
	if lock, ok := s.locks[id]; ok && lock.TryLock() {
		lock.Unlock()
		delete(s.locks, id)
	}
	path := s.sessionPath(id)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.logger.Warn("removing session note", "id", id, "err", err)
	}
}

func (s *Store) sessionPath(id string) string {
	return filepath.Join(s.dir, id+".md")
}
