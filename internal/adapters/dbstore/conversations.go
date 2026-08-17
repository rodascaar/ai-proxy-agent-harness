package dbstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ai-proxy-agent-harness/internal/core/conversation"
	"ai-proxy-agent-harness/internal/core/openai"
)

// conversationsSQL agrupa las sentencias preparadas de conversaciones.
var (
	convUpsertSQL = `INSERT INTO conversations (id, title, model, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = CASE WHEN conversations.title = '' THEN excluded.title ELSE conversations.title END,
			model = CASE WHEN excluded.model != '' THEN excluded.model ELSE conversations.model END,
			updated_at = excluded.updated_at`

	convListSQL = `SELECT c.id, c.title, c.model, c.updated_at,
			(SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id) AS message_count
		FROM conversations c
		ORDER BY c.updated_at DESC`

	convGetSQL = `SELECT id, title, model, created_at, updated_at FROM conversations WHERE id = ?`

	convMessagesSQL = `SELECT data FROM messages WHERE conversation_id = ? ORDER BY id`

	convRenameSQL = `UPDATE conversations SET title = ?, updated_at = ? WHERE id = ?`

	convDeleteSQL = `DELETE FROM conversations WHERE id = ?`

	msgInsertSQL = `INSERT INTO messages (conversation_id, role, data, created_at) VALUES (?, ?, ?, ?)`
)

// compile-time check: *Store implementa conversation.Store.
var _ conversation.Store = (*Store)(nil)

// List devuelve los resúmenes de todas las conversaciones, ordenadas por
// última actividad (más reciente primero), con el conteo de mensajes calculado
// en la propia query.
func (s *Store) List(ctx context.Context) []conversation.Summary {
	rows, err := s.db.QueryContext(ctx, convListSQL)
	if err != nil {
		s.logger.Error("listing conversations", "err", err)
		return nil
	}
	defer func() { _ = rows.Close() }()

	summaries := make([]conversation.Summary, 0, 16)
	for rows.Next() {
		var id, title, model, updatedRaw string
		var messageCount int
		if err := rows.Scan(&id, &title, &model, &updatedRaw, &messageCount); err != nil {
			s.logger.Error("scanning conversation summary", "err", err)
			continue
		}
		updatedAt, err := parseTime(updatedRaw)
		if err != nil {
			s.logger.Error("parsing conversation updated_at", "id", id, "err", err)
			continue
		}
		summaries = append(summaries, conversation.Summary{
			ID:            id,
			Title:         title,
			Model:         model,
			UpdatedAt:     updatedAt,
			MessagesCount: messageCount,
		})
	}
	return summaries
}

// Get devuelve una conversación completa (transcript de mensajes en orden).
// Devuelve conversation.ErrNotFound si no existe.
func (s *Store) Get(ctx context.Context, id string) (*conversation.Conversation, error) {
	conv, err := s.getConversationRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.loadMessages(ctx, conv); err != nil {
		return nil, err
	}
	return conv, nil
}

// Append agrega mensajes al final de la conversación. Si no existe, la crea de
// forma perezosa con un título derivado del primer mensaje user. Todo en una
// transacción: no se re-escribe el transcript entero, solo se insertan las
// filas nuevas.
func (s *Store) Append(ctx context.Context, id, model string, messages []openai.Message) (*conversation.Conversation, error) {
	if !conversation.ValidateID(id) {
		return nil, fmt.Errorf("invalid conversation id %q", id)
	}

	now := nowUTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin append conversation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	title := conversation.DeriveTitle(messages)
	if _, err := tx.Exec(convUpsertSQL, id, title, model, now, now); err != nil {
		return nil, fmt.Errorf("upserting conversation: %w", err)
	}
	for _, message := range messages {
		data, err := json.Marshal(message)
		if err != nil {
			return nil, fmt.Errorf("marshaling message: %w", err)
		}
		if _, err := tx.Exec(msgInsertSQL, id, string(message.Role), string(data), now); err != nil {
			return nil, fmt.Errorf("inserting message: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit append conversation: %w", err)
	}

	conv, err := s.getConversationRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.loadMessages(ctx, conv); err != nil {
		return nil, err
	}
	return conv, nil
}

// Rename actualiza el título de una conversación (espacios recortados; vacío
// se normaliza a "(sin título)").
func (s *Store) Rename(ctx context.Context, id, title string) (*conversation.Conversation, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "(sin título)"
	}
	result, err := s.db.ExecContext(ctx, convRenameSQL, title, nowUTC(), id)
	if err != nil {
		return nil, fmt.Errorf("renaming conversation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, conversation.ErrNotFound
	}
	conv, err := s.getConversationRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.loadMessages(ctx, conv); err != nil {
		return nil, err
	}
	return conv, nil
}

// Delete elimina la conversación y (por FK ON DELETE CASCADE) sus mensajes.
func (s *Store) Delete(ctx context.Context, id string) error {
	if !conversation.ValidateID(id) {
		return fmt.Errorf("invalid conversation id %q", id)
	}
	result, err := s.db.ExecContext(ctx, convDeleteSQL, id)
	if err != nil {
		return fmt.Errorf("deleting conversation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return conversation.ErrNotFound
	}
	return nil
}

// getConversationRow lee la fila de la conversación sin los mensajes.
func (s *Store) getConversationRow(ctx context.Context, id string) (*conversation.Conversation, error) {
	var conv conversation.Conversation
	var createdRaw, updatedRaw string
	err := s.db.QueryRowContext(ctx, convGetSQL, id).Scan(&conv.ID, &conv.Title, &conv.Model, &createdRaw, &updatedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, conversation.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting conversation: %w", err)
	}
	if conv.CreatedAt, err = parseTime(createdRaw); err != nil {
		return nil, fmt.Errorf("parsing conversation created_at: %w", err)
	}
	if conv.UpdatedAt, err = parseTime(updatedRaw); err != nil {
		return nil, fmt.Errorf("parsing conversation updated_at: %w", err)
	}
	return &conv, nil
}

// loadMessages carga el transcript de mensajes en orden de inserción.
func (s *Store) loadMessages(ctx context.Context, conv *conversation.Conversation) error {
	rows, err := s.db.QueryContext(ctx, convMessagesSQL, conv.ID)
	if err != nil {
		return fmt.Errorf("loading messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	conv.Messages = make([]openai.Message, 0, 8)
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return fmt.Errorf("scanning message: %w", err)
		}
		var message openai.Message
		if err := json.Unmarshal([]byte(data), &message); err != nil {
			return fmt.Errorf("decoding message: %w", err)
		}
		conv.Messages = append(conv.Messages, message)
	}
	return rows.Err()
}
