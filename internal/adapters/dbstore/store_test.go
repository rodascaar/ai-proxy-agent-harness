package dbstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"ai-proxy-agent-harness/internal/core/conversation"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/session"
)

// openTest abre un store en un directorio temporal aislado.
func openTest(t *testing.T, ttl time.Duration, maxSessions int) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "proxy.db"), ttl, maxSessions, nil)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func userMessage(text string) openai.Message {
	return openai.Message{Role: openai.RoleUser, Content: openai.NewTextContent(text)}
}

// sampleState arma un estado de sesión con checkpoint calculado del historial.
func sampleState(t testing.TB, messages []openai.Message) *session.State {
	t.Helper()
	chain, err := session.HashChain(messages)
	if err != nil {
		t.Fatalf("HashChain(): %v", err)
	}
	return &session.State{
		SessionID:      session.NewSessionID(),
		CheckpointHash: chain[len(chain)-1],
		CheckpointLen:  len(messages),
		Model:          "test-model",
		Results:        []string{"resultado"},
		TurnHistory:    []string{},
		LastUsedAt:     time.Now().UTC(),
	}
}

func TestSessionsPersistAndSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	messages := []openai.Message{userMessage("pregunta uno")}
	state := sampleState(t, messages)

	store, err := Open(filepath.Join(dir, "proxy.db"), time.Hour, 100, nil)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	_ = store.Close()

	store2, err := Open(filepath.Join(dir, "proxy.db"), time.Hour, 100, nil)
	if err != nil {
		t.Fatalf("reopen error: %v", err)
	}
	defer func() { _ = store2.Close() }()
	got, err := store2.FindMatching(context.Background(), messages)
	if err != nil {
		t.Fatalf("FindMatching() error: %v", err)
	}
	if got == nil || got.SessionID != state.SessionID {
		t.Fatalf("expected persisted session %s, got %#v", state.SessionID, got)
	}
	if len(got.Results) != 1 || got.Results[0] != "resultado" {
		t.Errorf("expected results roundtrip, got %#v", got.Results)
	}
}

func TestFindMatchingPrefersLongestCheckpoint(t *testing.T) {
	store := openTest(t, time.Hour, 100)

	turn1 := []openai.Message{userMessage("hola")}
	state1 := sampleState(t, turn1)
	turn2 := append(turn1, openai.Message{Role: openai.RoleAssistant, Content: openai.NewTextContent("respuesta")}, userMessage("y ahora"))
	state2 := sampleState(t, turn2)

	if err := store.Save(context.Background(), state1); err != nil {
		t.Fatalf("Save(1): %v", err)
	}
	if err := store.Save(context.Background(), state2); err != nil {
		t.Fatalf("Save(2): %v", err)
	}

	// La request con el prefijo completo debe devolver la sesión más larga.
	got, err := store.FindMatching(context.Background(), turn2)
	if err != nil {
		t.Fatalf("FindMatching(): %v", err)
	}
	if got == nil || got.SessionID != state2.SessionID {
		t.Fatalf("expected longest session %s, got %#v", state2.SessionID, got)
	}
}

func TestFindMatchingMatchesPrefixPositionally(t *testing.T) {
	store := openTest(t, time.Hour, 100)

	// El checkpoint de una sesión es el hash en SU posición, no el del final
	// de la request: una request con un turno nuevo más largo debe casar con
	// la sesión cuyo prefijo coincide.
	prefix := []openai.Message{userMessage("contexto")}
	state := sampleState(t, prefix)
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	newTurn := append(append([]openai.Message{}, prefix...),
		openai.Message{Role: openai.RoleAssistant, Content: openai.NewTextContent("respuesta")},
		userMessage("siguiente turno"))
	got, err := store.FindMatching(context.Background(), newTurn)
	if err != nil {
		t.Fatalf("FindMatching(): %v", err)
	}
	if got == nil || got.SessionID != state.SessionID {
		t.Fatalf("expected prefix match to return the session, got %#v", got)
	}
}

func TestFindMatchingTTLFiltersExpired(t *testing.T) {
	store := openTest(t, time.Hour, 100)
	messages := []openai.Message{userMessage("efímera")}
	state := sampleState(t, messages)
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	// Los timestamps persisten con precisión de segundo (RFC3339), así que un
	// TTL sub-segundo no filtra de forma determinista; simulamos el paso del
	// tiempo envejeciendo la fila directamente.
	if _, err := store.db.Exec(`UPDATE sessions SET last_used_at = ? WHERE session_id = ?`,
		"2020-01-01T00:00:00Z", state.SessionID); err != nil {
		t.Fatalf("aging session: %v", err)
	}
	got, err := store.FindMatching(context.Background(), messages)
	if err != nil {
		t.Fatalf("FindMatching(): %v", err)
	}
	if got != nil {
		t.Errorf("expected expired session to be filtered out, got session %s", got.SessionID)
	}
}

func TestSaveEvictsBeyondMaxSessions(t *testing.T) {
	store := openTest(t, time.Hour, 3)
	for i := 0; i < 5; i++ {
		messages := []openai.Message{userMessage(string(rune('a' + i)))}
		state := sampleState(t, messages)
		if err := store.Save(context.Background(), state); err != nil {
			t.Fatalf("Save(%d): %v", i, err)
		}
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 sessions after overflow eviction, got %d", count)
	}
}

func TestLockIsStablePerSession(t *testing.T) {
	store := openTest(t, time.Hour, 100)
	a := store.Lock("aaa")
	b := store.Lock("aaa")
	if a != b {
		t.Errorf("expected the same mutex for the same session id")
	}
	c := store.Lock("bbb")
	if c == a {
		t.Errorf("expected a distinct mutex for a different session")
	}
}

func TestConversationsCRUD(t *testing.T) {
	store := openTest(t, time.Hour, 100)
	ctx := context.Background()
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	conv, err := store.Append(ctx, id, "test-model", []openai.Message{userMessage("hola")})
	if err != nil {
		t.Fatalf("Append(): %v", err)
	}
	if conv.Title != "hola" {
		t.Errorf("expected derived title, got %q", conv.Title)
	}
	if len(conv.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(conv.Messages))
	}

	// Segunda ronda: no duplica lo previo.
	conv, err = store.Append(ctx, id, "test-model", []openai.Message{
		{Role: openai.RoleAssistant, Content: openai.NewTextContent("respuesta")},
		userMessage("siguiente"),
	})
	if err != nil {
		t.Fatalf("Append(2): %v", err)
	}
	if len(conv.Messages) != 3 {
		t.Fatalf("expected 3 messages after second append, got %d", len(conv.Messages))
	}
	if conv.Messages[2].Content.Text != "siguiente" {
		t.Errorf("unexpected last message %#v", conv.Messages[2])
	}

	list := store.List(ctx)
	if len(list) != 1 || list[0].MessagesCount != 3 {
		t.Errorf("expected 1 conversation with 3 messages, got %#v", list)
	}
	if list[0].ID != id {
		t.Errorf("expected conversation id %s, got %s", id, list[0].ID)
	}

	conv, err = store.Rename(ctx, id, "Título nuevo")
	if err != nil {
		t.Fatalf("Rename(): %v", err)
	}
	if conv.Title != "Título nuevo" {
		t.Errorf("expected renamed title, got %q", conv.Title)
	}

	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	if _, err := store.Get(ctx, id); !errors.Is(err, conversation.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestConversationMessageRoundtripMultimodal(t *testing.T) {
	store := openTest(t, time.Hour, 100)
	ctx := context.Background()
	id := "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee"

	// Parte de imagen con su raw original (data: URL) que debe sobrevivir la
	// serialización a JSON en messages.data sin re-encodificar.
	rich := openai.Message{
		Role: openai.RoleUser,
		Content: &openai.Content{
			Text: "describe",
			Parts: []openai.ContentPart{{
				Type:     "image_url",
				ImageURL: &openai.ImageURL{URL: "data:image/png;base64,AAAA"},
				Raw:      json.RawMessage(`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}`),
			}},
		},
	}
	if _, err := store.Append(ctx, id, "test-model", []openai.Message{rich}); err != nil {
		t.Fatalf("Append(): %v", err)
	}
	conv, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}
	if len(conv.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(conv.Messages))
	}
	got, err := json.Marshal(conv.Messages[0])
	if err != nil {
		t.Fatalf("marshaling stored message: %v", err)
	}
	want, err := json.Marshal(rich)
	if err != nil {
		t.Fatalf("marshaling original message: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("multimodal message did not survive roundtrip\n got %s\nwant %s", got, want)
	}
}

func TestValidateIDRules(t *testing.T) {
	valid := []string{
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"abcDEF123-_",
	}
	invalid := []string{
		"", "a", "short",
		"con espacio", "con/slash", "con.backslash",
	}
	for _, id := range valid {
		if !conversation.ValidateID(id) {
			t.Errorf("expected %q to be valid", id)
		}
	}
	for _, id := range invalid {
		if conversation.ValidateID(id) {
			t.Errorf("expected %q to be invalid", id)
		}
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		store, err := Open(filepath.Join(dir, "proxy.db"), time.Hour, 100, nil)
		if err != nil {
			t.Fatalf("Open(%d): %v", i, err)
		}
		_ = store.Close()
	}
	var versions int
	store, err := Open(filepath.Join(dir, "proxy.db"), time.Hour, 100, nil)
	if err != nil {
		t.Fatalf("Open(3): %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&versions); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if versions != 2 {
		t.Errorf("expected 2 applied migrations, got %d", versions)
	}
}
