package conversationstore

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-proxy-agent-harness/internal/core/openai"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := New(dir, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	return store
}

func userMessage(text string) openai.Message {
	return openai.Message{Role: openai.RoleUser, Content: openai.NewTextContent(text)}
}

func assistantMessage(text string) openai.Message {
	return openai.Message{Role: openai.RoleAssistant, Content: openai.NewTextContent(text)}
}

func TestAppendCreatesLazilyAndDerivesTitle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	conv, err := store.Append(ctx, "abc12345", "qwen2.5:7b", []openai.Message{userMessage("Primera línea\nsegunda línea")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if conv.Title != "Primera línea" {
		t.Errorf("expected title from first line, got %q", conv.Title)
	}
	if conv.Model != "qwen2.5:7b" {
		t.Errorf("expected model recorded, got %q", conv.Model)
	}
	if len(conv.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(conv.Messages))
	}

	// Título derivado solo la primera vez; el segundo append lo conserva.
	_, err = store.Append(ctx, "abc12345", "", []openai.Message{assistantMessage("respuesta")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := store.Get(ctx, "abc12345")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Primera línea" {
		t.Errorf("title changed after second append: %q", got.Title)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got.Messages))
	}
}

func TestListOrdersByMostRecent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Append(ctx, "aaaaaaaa", "m1", []openai.Message{userMessage("vieja")})
	if err != nil {
		t.Fatalf("append a: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	_, err = store.Append(ctx, "bbbbbbbb", "m2", []openai.Message{userMessage("reciente")})
	if err != nil {
		t.Fatalf("append b: %v", err)
	}

	list := store.List(ctx)
	if len(list) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(list))
	}
	if list[0].ID != "bbbbbbbb" || list[1].ID != "aaaaaaaa" {
		t.Errorf("expected newest first, got %q then %q", list[0].ID, list[1].ID)
	}
	if list[0].MessagesCount != 1 {
		t.Errorf("expected messages_count=1, got %d", list[0].MessagesCount)
	}
}

func TestRenameAndDelete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.Append(ctx, "cccccccc", "m", []openai.Message{userMessage("hola")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	renamed, err := store.Rename(ctx, "cccccccc", "  Nuevo título  ")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.Title != "Nuevo título" {
		t.Errorf("expected trimmed title, got %q", renamed.Title)
	}

	if err := store.Delete(ctx, "cccccccc"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, "cccccccc"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if _, err := store.Rename(ctx, "cccccccc", "x"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound on rename of missing, got %v", err)
	}
}

func TestReloadFromDisk(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	store, err := New(dir, logger)
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	ctx := context.Background()
	if _, err := store.Append(ctx, "dddddddd", "m", []openai.Message{userMessage("persistir")}); err != nil {
		t.Fatalf("append: %v", err)
	}

	reopened, err := New(dir, logger)
	if err != nil {
		t.Fatalf("reopening store: %v", err)
	}
	conv, err := reopened.Get(ctx, "dddddddd")
	if err != nil {
		t.Fatalf("get after reload: %v", err)
	}
	if len(conv.Messages) != 1 || conv.Messages[0].Content.Text != "persistir" {
		t.Errorf("expected reloaded transcript, got %+v", conv.Messages)
	}
}

func TestSkipsCorruptFilesOnLoad(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	bad := filepath.Join(dir, "eeeeeeee.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}

	store, err := New(dir, logger)
	if err != nil {
		t.Fatalf("creating store with corrupt file: %v", err)
	}
	if len(store.List(context.Background())) != 0 {
		t.Errorf("expected corrupt file skipped")
	}
}

func TestValidateID(t *testing.T) {
	valid := []string{"abc12345", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "ABC_123-xyz"}
	for _, id := range valid {
		if !ValidateID(id) {
			t.Errorf("expected %q valid", id)
		}
	}
	invalid := []string{"", "..", "../etc/passwd", "a", strings.Repeat("a", 65), "id with space", "a/b"}
	for _, id := range invalid {
		if ValidateID(id) {
			t.Errorf("expected %q invalid", id)
		}
	}
}

func TestAppendRejectsInvalidID(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Append(context.Background(), "../escape", "m", []openai.Message{userMessage("x")}); err == nil {
		t.Errorf("expected error for path traversal id")
	}
}

func TestRoundTripMultimodalMessages(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	img := openai.NewImagePart("data:image/png;base64,AAAA")
	msg := openai.Message{Role: openai.RoleUser, Content: &openai.Content{
		Text:  "mira esto",
		Parts: []openai.ContentPart{img},
	}}

	if _, err := store.Append(ctx, "ffffffff", "m", []openai.Message{msg}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := store.Get(ctx, "ffffffff")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Messages[0].Content.Parts) != 1 {
		t.Fatalf("expected 1 image part in memory, got %d", len(got.Messages[0].Content.Parts))
	}

	// Verifica que sobrevive un round-trip por disco (json round-trip).
	buf, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Conversation
	if err := json.Unmarshal(buf, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Messages) != 1 || len(decoded.Messages[0].Content.Parts) != 1 {
		t.Errorf("expected image part to survive json round-trip")
	}
}
