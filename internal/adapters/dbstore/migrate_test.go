package dbstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestImportLegacySessions carga una nota markdown del formato anterior
// (resumen + fence ```json canónico) y la deja disponible en SQLite.
func TestImportLegacySessions(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, legacySessionsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	note := `# Sesión abc123

- **Modelo**: qwen2.5:7b

## Estado (fuente de verdad)

` + "```json\n" + `{"session_id":"abc123","checkpoint_hash":"h","checkpoint_len":1,"goal_ctx":{},"model":"qwen2.5:7b","results":["ok"],"turn_history":null,"last_used_at":"2026-01-01T00:00:00Z"}
` + "```\n"
	if err := os.WriteFile(filepath.Join(dir, legacySessionsDir, "abc123.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	// Corrupta: sin fence válido → se ignora con warning.
	if err := os.WriteFile(filepath.Join(dir, legacySessionsDir, "corrupta.md"), []byte("# sin json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(dir) })
	// El import legado lee los directorios relativos a cwd.
	store, err := Open(filepath.Join(dir, "proxy.db"), time.Hour, 100, nil)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = store.Close() }()

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 imported session, got %d", count)
	}
}

// TestImportLegacyConversations carga un ledger JSON del formato anterior y
// lo deja disponible en SQLite (conversación + mensajes).
func TestImportLegacyConversations(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, legacyConversationsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := `{
		"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"title": "Importada",
		"model": "qwen2.5:7b",
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
		"messages": [
			{"role": "user", "content": "hola"},
			{"role": "assistant", "content": "adiós"}
		]
	}`
	if err := os.WriteFile(filepath.Join(dir, legacyConversationsDir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.json"), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(dir) })

	store, err := Open(filepath.Join(dir, "proxy.db"), time.Hour, 100, nil)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = store.Close() }()

	conv, err := store.Get(context.Background(), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if conv.Title != "Importada" {
		t.Errorf("expected imported title, got %q", conv.Title)
	}
	if len(conv.Messages) != 2 || conv.Messages[1].Content.Text != "adiós" {
		t.Errorf("expected 2 imported messages, got %#v", conv.Messages)
	}
}

// TestImportLegacySkipsWhenDataExists asegura que el import es idempotente: con
// datos ya presentes en la base no vuelve a importar.
func TestImportLegacySkipsWhenDataExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(dir) })

	// Primera apertura SIN datos legados: se crea la base vacía.
	store, err := Open(filepath.Join(dir, "proxy.db"), time.Hour, 100, nil)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	// Nueva sesión propia: la tabla ya no está vacía → no se importará la nota.
	_, _ = store.db.Exec(`INSERT INTO sessions (session_id, checkpoint_hash, checkpoint_len, state_json, last_used_at)
		VALUES ('owned', 'x', 1, '{}', '2026-01-01T00:00:00Z')`)
	_ = store.Close()

	// Aparecen los directorios legados y la base ya tiene datos.
	if err := os.MkdirAll(filepath.Join(dir, legacySessionsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	note := "```json\n" + `{"session_id":"abc123","checkpoint_hash":"h","checkpoint_len":1,"goal_ctx":{},"model":"m","results":["ok"],"turn_history":null,"last_used_at":"2026-01-01T00:00:00Z"}` + "\n```\n"
	if err := os.WriteFile(filepath.Join(dir, legacySessionsDir, "abc123.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}

	store2, err := Open(filepath.Join(dir, "proxy.db"), time.Hour, 100, nil)
	if err != nil {
		t.Fatalf("reopen error: %v", err)
	}
	defer func() { _ = store2.Close() }()
	var count int
	if err := store2.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE session_id = 'abc123'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected legacy import to be skipped, got %d imported", count)
	}
}

func TestExtractJSONFence(t *testing.T) {
	raw, ok := extractJSONFence("texto\n" + "```json\n" + `{"a":1}` + "\n```\n")
	if !ok || string(raw) != `{"a":1}` {
		t.Fatalf("unexpected extract: ok=%v raw=%q", ok, raw)
	}
	if _, ok := extractJSONFence("sin fence"); ok {
		t.Error("expected ok=false without a fence")
	}
	if _, ok := extractJSONFence("```json\nabierto"); ok {
		t.Error("expected ok=false with an open fence")
	}
	if _, ok := extractJSONFence("```json\n{}\n```\n```json\n{}\n```"); !ok {
		t.Error("expected the last well-formed fence to win")
	}
}

// TestNoLegacyDirsIsSilent arranca limpio cuando no existen directorios viejos.
func TestNoLegacyDirsIsSilent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(dir) })
	store, err := Open(filepath.Join(dir, "proxy.db"), time.Hour, 100, nil)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = store.Close() }()
	var count int
	_ = fmt.Sprint()
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected no sessions, got %d", count)
	}
}
