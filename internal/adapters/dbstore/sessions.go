package dbstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/session"
)

// sessionsSQL agrupa las sentencias preparadas de sesiones.
var (
	sessionsUpsertSQL = `INSERT INTO sessions (session_id, checkpoint_hash, checkpoint_len, state_json, last_used_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			checkpoint_hash = excluded.checkpoint_hash,
			checkpoint_len  = excluded.checkpoint_len,
			state_json      = excluded.state_json,
			last_used_at    = excluded.last_used_at`

	sessionsDeleteTTLSQL = `DELETE FROM sessions WHERE last_used_at < ?`

	sessionsCountSQL = `SELECT COUNT(*) FROM sessions`

	sessionsEvictOldestSQL = `DELETE FROM sessions WHERE session_id IN (
		SELECT session_id FROM sessions ORDER BY last_used_at ASC LIMIT ?)`
)

// compile-time check: *Store implementa session.Store.
var _ session.Store = (*Store)(nil)

// FindMatching devuelve la sesión con el checkpoint de mayor longitud que
// coincida con un prefijo del historial de mensajes, o nil si no hay ninguna.
//
// El checkpoint de una sesión identifica el historial messages[:checkpoint_len],
// o sea que debe compararse contra chain[checkpoint_len-1] (el hash en ESA
// posición del prefijo), no contra el último hash. Para no escanear, se
// materializa la cadena como tabla VALUES (pos, hash) en la propia query: el
// planificador la junta con el índice (checkpoint_hash, checkpoint_len) y solo
// devuelve la fila de mayor checkpoint_len. Las sesiones expiradas se filtran
// en la query, sin escribir en la ruta de lectura.
func (s *Store) FindMatching(ctx context.Context, messages []openai.Message) (*session.State, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	chain, err := session.HashChain(messages)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString(`SELECT state_json FROM sessions
		JOIN (`)
	args := make([]any, 0, 2*len(chain)+1)
	for i, hash := range chain {
		if i > 0 {
			b.WriteString(" UNION ALL")
		}
		b.WriteString(" SELECT ? AS pos, ? AS h")
		args = append(args, i+1, hash)
	}
	b.WriteString(`) AS c ON c.pos = sessions.checkpoint_len AND c.h = sessions.checkpoint_hash
		WHERE sessions.last_used_at >= ?
		ORDER BY sessions.checkpoint_len DESC LIMIT 1`)
	args = append(args, ttlCutoff(s.ttl))

	var raw string
	err = s.db.QueryRowContext(ctx, b.String(), args...).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding matching session: %w", err)
	}
	var state session.State
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("decoding session state: %w", err)
	}
	return &state, nil
}

// Save persiste (o actualiza) la sesión y aplica la evicción por TTL y por
// max_sessions en la misma transacción.
func (s *Store) Save(ctx context.Context, state *session.State) error {
	if state.SessionID == "" {
		return errors.New("cannot save session without id")
	}
	state.LastUsedAt = time.Now().UTC()
	return s.insertState(state)
}

// insertState escribe la fila de sesión y aplica evicciones. Lo comparte Save
// con el import legado.
func (s *Store) insertState(state *session.State) error {
	if err := s.saveAndEvict(state); err != nil {
		return err
	}
	s.logger.Debug("session saved", "session_id", state.SessionID, "checkpoint_len", state.CheckpointLen)
	return nil
}

func (s *Store) saveAndEvict(state *session.State) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshaling session state: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin save session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(sessionsUpsertSQL, state.SessionID, state.CheckpointHash, state.CheckpointLen, string(raw), formatTime(state.LastUsedAt)); err != nil {
		return fmt.Errorf("upserting session: %w", err)
	}
	if s.ttl > 0 {
		if _, err := tx.Exec(sessionsDeleteTTLSQL, ttlCutoff(s.ttl)); err != nil {
			return fmt.Errorf("evicting expired sessions: %w", err)
		}
	}
	if err := s.evictOverflowLocked(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// evictOverflowLocked borra las sesiones más viejas cuando se supera
// max_sessions. Se ejecuta dentro de la transacción de Save.
func (s *Store) evictOverflowLocked(tx *sql.Tx) error {
	var count int
	if err := tx.QueryRow(sessionsCountSQL).Scan(&count); err != nil {
		return fmt.Errorf("counting sessions: %w", err)
	}
	overflow := count - s.maxSessions
	if overflow <= 0 {
		return nil
	}
	if _, err := tx.Exec(sessionsEvictOldestSQL, overflow); err != nil {
		return fmt.Errorf("evicting overflowing sessions: %w", err)
	}
	return nil
}

// Lock devuelve el mutex por-sesión para serializar reanudaciones concurrentes
// de la misma sesión (la vía resume del servicio los adquiere antes de
// decidir, evitando doble ejecución de un run pausado).
func (s *Store) Lock(sessionID string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	lock, ok := s.locks[sessionID]
	if !ok {
		lock = &sync.Mutex{}
		s.locks[sessionID] = lock
	}
	return lock
}

// ttlCutoff devuelve el límite de expiración (RFC3339 UTC). ttl <= 0 devuelve
// un timestamp en el futuro para que el filtro no excluya nada.
func ttlCutoff(ttl time.Duration) string {
	if ttl <= 0 {
		return time.Now().UTC().Add(365 * 24 * time.Hour).Format(timeLayout)
	}
	return time.Now().UTC().Add(-ttl).Format(timeLayout)
}
