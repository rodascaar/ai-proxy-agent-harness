package dbstore

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-proxy-agent-harness/internal/core/conversation"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/session"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// legacySessionsDir y legacyConversationsDir son los directorios que usaban
// los adaptadores anteriores (notas markdown y ledger JSON). El import legado
// los lee una sola vez si existen y la base está vacía.
const (
	legacySessionsDir      = ".sessions"
	legacyConversationsDir = "conversations"
)

// migrate aplica las migraciones SQL embebidas en orden, rastreando la versión
// en schema_migrations. Cada migración corre en su propia transacción.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)

	for _, name := range entries {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		applied, err := s.isApplied(version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		raw, err := migrationsFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(raw)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("applying migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, nowUTC()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("recording migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
		s.logger.Info("applied migration", "version", version)
	}
	return nil
}

// migrationVersion extrae el número de versión del prefijo numérico del
// nombre (ej. "001_init.sql" -> 1).
func migrationVersion(name string) (int, error) {
	base := filepath.Base(name)
	idx := strings.Index(base, "_")
	if idx <= 0 {
		return 0, fmt.Errorf("migration %s: name must start with a numeric prefix", name)
	}
	version, err := strconv.Atoi(base[:idx])
	if err != nil {
		return 0, fmt.Errorf("migration %s: invalid version prefix: %w", name, err)
	}
	return version, nil
}

func (s *Store) isApplied(version int) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// importLegacy migra los datos de los adaptadores anteriores (notas
// .sessions/*.md y conversations/*.json) a SQLite, una única vez y solo si la
// base está vacía. Es idempotente: con datos ya presentes o directorios
// inexistentes no hace nada.
func (s *Store) importLegacy() error {
	importedSessions := 0
	if empty, err := s.tableEmpty("sessions"); err != nil {
		return err
	} else if empty {
		importedSessions, err = s.importLegacySessions()
		if err != nil {
			return err
		}
	}

	importedConversations := 0
	if empty, err := s.tableEmpty("conversations"); err != nil {
		return err
	} else if empty {
		importedConversations, err = s.importLegacyConversations()
		if err != nil {
			return err
		}
	}

	if importedSessions > 0 || importedConversations > 0 {
		s.logger.Info("imported legacy data", "sessions", importedSessions, "conversations", importedConversations)
	}
	return nil
}

func (s *Store) tableEmpty(table string) (bool, error) {
	var count int
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}

// importLegacySessions lee las notas markdown de .sessions/ y reinserta su
// bloque JSON canónico como filas de sessions.
func (s *Store) importLegacySessions() (int, error) {
	entries, err := os.ReadDir(legacySessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading legacy sessions dir: %w", err)
	}

	imported := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(legacySessionsDir, entry.Name()))
		if err != nil {
			s.logger.Warn("skipping unreadable legacy session note", "file", entry.Name(), "err", err)
			continue
		}
		jsonRaw, ok := extractJSONFence(string(raw))
		if !ok {
			s.logger.Warn("skipping legacy session note without json fence", "file", entry.Name())
			continue
		}
		var state session.State
		if err := json.Unmarshal(jsonRaw, &state); err != nil {
			s.logger.Warn("skipping corrupt legacy session note", "file", entry.Name(), "err", err)
			continue
		}
		// La nota trae el last_used_at histórico; si quedara intacto, la
		// evicción por TTL del primer Save lo borraría al instante. Lo
		// reetiquetamos como "en uso ahora".
		state.LastUsedAt = time.Now().UTC()
		if err := s.insertState(&state); err != nil {
			return imported, fmt.Errorf("importing legacy session %s: %w", entry.Name(), err)
		}
		imported++
	}
	return imported, nil
}

// legacyConversation es el formato del ledger JSON de los adaptadores
// anteriores (conversationstore).
type legacyConversation struct {
	ID        string           `json:"id"`
	Title     string           `json:"title"`
	Model     string           `json:"model"`
	CreatedAt string           `json:"created_at"`
	UpdatedAt string           `json:"updated_at"`
	Messages  []openai.Message `json:"messages"`
}

// importLegacyConversations lee los ledger JSON de conversations/ y los
// reinserta como filas de conversations + messages.
func (s *Store) importLegacyConversations() (int, error) {
	entries, err := os.ReadDir(legacyConversationsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading legacy conversations dir: %w", err)
	}

	imported := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(legacyConversationsDir, entry.Name()))
		if err != nil {
			s.logger.Warn("skipping unreadable legacy conversation", "file", entry.Name(), "err", err)
			continue
		}
		var legacy legacyConversation
		if err := json.Unmarshal(raw, &legacy); err != nil {
			s.logger.Warn("skipping corrupt legacy conversation", "file", entry.Name(), "err", err)
			continue
		}
		if legacy.ID == "" || !conversation.ValidateID(legacy.ID) {
			s.logger.Warn("skipping legacy conversation without valid id", "file", entry.Name())
			continue
		}
		if err := s.insertLegacyConversation(&legacy); err != nil {
			return imported, fmt.Errorf("importing legacy conversation %s: %w", entry.Name(), err)
		}
		imported++
	}
	return imported, nil
}

func (s *Store) insertLegacyConversation(legacy *legacyConversation) error {
	createdAt := legacy.CreatedAt
	if createdAt == "" {
		createdAt = nowUTC()
	}
	updatedAt := legacy.UpdatedAt
	if updatedAt == "" {
		updatedAt = createdAt
	}
	title := legacy.Title
	if title == "" {
		title = "(sin título)"
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT INTO conversations (id, title, model, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		legacy.ID, title, legacy.Model, createdAt, updatedAt); err != nil {
		return err
	}
	for _, message := range legacy.Messages {
		data, err := json.Marshal(message)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO messages (conversation_id, role, data, created_at) VALUES (?, ?, ?, ?)`,
			legacy.ID, string(message.Role), string(data), createdAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// stateFencePrefix abre el bloque JSON canónico dentro de cada nota markdown.
const stateFencePrefix = "```json"

// extractJSONFence devuelve el contenido del ÚLTIMO fence ```json bien
// formado (la fuente de verdad canónica de las notas). Devuelve ok=false si no
// hay ninguno o si algún fence queda abierto.
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
