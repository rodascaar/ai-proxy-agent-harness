package md

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/session"
	"ai-proxy-agent-harness/internal/core/task"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseMessages() []openai.Message {
	return []openai.Message{
		{Role: openai.RoleSystem, Content: openai.NewTextContent("sistema")},
		{Role: openai.RoleUser, Content: openai.NewTextContent("hola")},
	}
}

func checkpointState(t *testing.T, id string, messages []openai.Message) *session.State {
	t.Helper()
	chain, err := session.HashChain(messages)
	if err != nil {
		t.Fatalf("HashChain() error: %v", err)
	}
	return &session.State{
		SessionID:      id,
		CheckpointHash: chain[len(chain)-1],
		CheckpointLen:  len(messages),
		Model:          "m",
		LastUsedAt:     time.Now(),
	}
}

func TestSaveAndFindMatching(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, time.Minute, 10, noopLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	messages := baseMessages()
	state := checkpointState(t, "abc", messages)
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	got, err := store.FindMatching(context.Background(), messages)
	if err != nil {
		t.Fatalf("FindMatching() error: %v", err)
	}
	if got == nil || got.SessionID != "abc" {
		t.Fatalf("expected session abc, got %#v", got)
	}

	// Mensajes distintos no deben matchear.
	other, err := store.FindMatching(context.Background(), []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("otra cosa")}})
	if err != nil {
		t.Fatalf("FindMatching() error: %v", err)
	}
	if other != nil {
		t.Errorf("expected no match, got %#v", other)
	}
}

func TestFindMatchingPicksLongestCheckpoint(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, time.Minute, 10, noopLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	short := baseMessages()
	long := append(append([]openai.Message{}, short...), openai.Message{Role: openai.RoleAssistant, Content: openai.NewTextContent("respuesta")})

	if err := store.Save(context.Background(), checkpointState(t, "short", short)); err != nil {
		t.Fatalf("Save(short) error: %v", err)
	}
	if err := store.Save(context.Background(), checkpointState(t, "long", long)); err != nil {
		t.Fatalf("Save(long) error: %v", err)
	}

	got, err := store.FindMatching(context.Background(), long)
	if err != nil {
		t.Fatalf("FindMatching() error: %v", err)
	}
	if got == nil || got.SessionID != "long" {
		t.Fatalf("expected longest checkpoint match (long), got %#v", got)
	}
}

func TestReloadFromDiskAfterRestart(t *testing.T) {
	dir := t.TempDir()
	messages := baseMessages()

	store1, err := New(dir, time.Minute, 10, noopLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := store1.Save(context.Background(), checkpointState(t, "persistida", messages)); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Simula un reinicio: instancia nueva sobre el mismo directorio.
	store2, err := New(dir, time.Minute, 10, noopLogger())
	if err != nil {
		t.Fatalf("New() after restart error: %v", err)
	}
	got, err := store2.FindMatching(context.Background(), messages)
	if err != nil {
		t.Fatalf("FindMatching() error: %v", err)
	}
	if got == nil || got.SessionID != "persistida" {
		t.Fatalf("expected session recovered after restart, got %#v", got)
	}
}

func TestEvictionByMaxSessionsRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, time.Minute, 2, noopLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := store.Save(context.Background(), checkpointState(t, "a", baseMessages())); err != nil {
		t.Fatalf("Save(a) error: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := store.Save(context.Background(), checkpointState(t, "b", baseMessages())); err != nil {
		t.Fatalf("Save(b) error: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := store.Save(context.Background(), checkpointState(t, "c", baseMessages())); err != nil {
		t.Fatalf("Save(c) error: %v", err)
	}

	// "a" es la más vieja → debe ser evictada y su .md borrado.
	if _, err := os.Stat(filepath.Join(dir, "a.md")); !os.IsNotExist(err) {
		t.Errorf("expected a.md removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.md")); err != nil {
		t.Errorf("expected b.md present, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.md")); err != nil {
		t.Errorf("expected c.md present, err=%v", err)
	}
}

func TestEvictionByTTL(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, 10*time.Millisecond, 10, noopLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := store.Save(context.Background(), checkpointState(t, "vieja", baseMessages())); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	got, err := store.FindMatching(context.Background(), baseMessages())
	if err != nil {
		t.Fatalf("FindMatching() error: %v", err)
	}
	if got != nil {
		t.Errorf("expected expired session evicted, got %#v", got)
	}
}

func TestLockPerSession(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, time.Minute, 10, noopLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	first := store.Lock("x")
	second := store.Lock("x")
	if first != second {
		t.Errorf("Lock(x) must return the same mutex")
	}
	if store.Lock("x") == store.Lock("y") {
		t.Errorf("Lock(x) and Lock(y) must differ")
	}
	if store.Lock("x") == nil {
		t.Errorf("Lock must not return nil")
	}
}

func TestNoteFormatIsReadable(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, time.Minute, 10, noopLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	state := checkpointState(t, "nota", baseMessages())
	root := task.NewNode("raíz", 0)
	root.AddChild(task.NewNode("tarea a", 1)).IsAtomic = true
	root.AddChild(task.NewNode("tarea b", 1)).IsAtomic = true
	state.Root = root
	state.Results = []string{"resultado de tarea a"}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "nota.md"))
	if err != nil {
		t.Fatalf("reading note: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "# Sesión nota") {
		t.Errorf("note should start with a readable heading")
	}
	if !strings.Contains(content, "## Estado (fuente de verdad)") {
		t.Errorf("note should contain the JSON source-of-truth section")
	}
	if !strings.Contains(content, "```json") {
		t.Errorf("note should contain a json fence")
	}
	if !strings.Contains(content, "tarea a (atómica)") {
		t.Errorf("note should render the task tree")
	}
	if !strings.Contains(content, "resultado de tarea a") {
		t.Errorf("note should list the results")
	}
}

func TestCorruptNoteIsSkippedOnLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "basura.md"), []byte("no es json"), 0o644); err != nil {
		t.Fatalf("writing corrupt note: %v", err)
	}
	store, err := New(dir, time.Minute, 10, noopLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if len(store.byID) != 0 {
		t.Errorf("corrupt notes must be skipped, loaded %d", len(store.byID))
	}
}

func TestSaveRequiresSessionID(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, time.Minute, 10, noopLogger())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := store.Save(context.Background(), &session.State{}); err == nil {
		t.Errorf("expected error saving session without id")
	}
}
