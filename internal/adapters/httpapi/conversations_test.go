package httpapi_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-proxy-agent-harness/internal/adapters/conversationstore"
	"ai-proxy-agent-harness/internal/adapters/httpapi"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/testutil/fakellm"
)

// newTestServerWithConversations construye el handler de tests con un
// conversationstore real en un directorio temporal.
func newTestServerWithConversations(t *testing.T, llm *fakellm.Fake) (*httpapi.Server, *conversationstore.Store) {
	t.Helper()
	handler, _ := newTestServer(t, llm, true)
	store, err := conversationstore.New(t.TempDir(), noopLogger())
	if err != nil {
		t.Fatalf("conversationstore.New(): %v", err)
	}
	handler.SetConversationStore(store)
	return handler, store
}

// doJSONWithHeader es doJSON pero con un header custom (p. ej. el
// X-Conversation-ID del chat).
func doJSONWithHeader(t *testing.T, handler http.Handler, method, path, body, headerName, headerValue string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if headerName != "" {
		req.Header.Set(headerName, headerValue)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func chatRequestBody(first, second string) string {
	if second == "" {
		return `{"model":"test-model","messages":[{"role":"user","content":"` + first + `"}]}`
	}
	return `{"model":"test-model","messages":[
		{"role":"user","content":"` + first + `"},
		{"role":"assistant","content":"respuesta"},
		{"role":"user","content":"` + second + `"}
	]}`
}

const testConversationID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func TestChatRecordsConversationWithHeader(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"leaf"}, nil).
		StreamResponse([]string{"respuesta"}, nil).
		Completion(atomicCompletion()).
		StreamResponse([]string{"leaf2"}, nil).
		StreamResponse([]string{"respuesta2"}, nil)
	handler, store := newTestServerWithConversations(t, llm)

	rec := doJSONWithHeader(t, handler, http.MethodPost, "/v1/chat/completions",
		chatRequestBody("primera pregunta", ""), "X-Conversation-ID", testConversationID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Conversación creada de forma perezosa con user + assistant.
	list := store.List(reqCtx())
	if len(list) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(list))
	}
	if list[0].ID != testConversationID {
		t.Errorf("expected conversation id %s, got %s", testConversationID, list[0].ID)
	}
	conv, err := store.Get(reqCtx(), list[0].ID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.Title != "primera pregunta" {
		t.Errorf("expected title derived from first message, got %q", conv.Title)
	}
	if len(conv.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(conv.Messages))
	}
	if conv.Messages[0].Role != openai.RoleUser || conv.Messages[0].Content.Text != "primera pregunta" {
		t.Errorf("unexpected user message %#v", conv.Messages[0])
	}
	if conv.Messages[1].Role != openai.RoleAssistant || conv.Messages[1].Content.Text != "respuesta" {
		t.Errorf("unexpected assistant message %#v", conv.Messages[1])
	}
}

func TestSecondTurnAppendsWithoutDuplicating(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"leaf1"}, nil).
		StreamResponse([]string{"respuesta1"}, nil).
		Completion(atomicCompletion()).
		StreamResponse([]string{"leaf2"}, nil).
		StreamResponse([]string{"respuesta2"}, nil)
	handler, store := newTestServerWithConversations(t, llm)

	doJSONWithHeader(t, handler, http.MethodPost, "/v1/chat/completions",
		chatRequestBody("primera pregunta", ""), "X-Conversation-ID", testConversationID)
	convID := testConversationID

	// Turno 2 con el mismo header: no debe duplicar el historial previo.
	rec := doJSONWithHeader(t, handler, http.MethodPost, "/v1/chat/completions",
		chatRequestBody("primera pregunta", "segunda pregunta"), "X-Conversation-ID", convID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	conv, err := store.Get(reqCtx(), convID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if len(conv.Messages) != 4 {
		t.Fatalf("expected 4 messages (2 turns), got %d", len(conv.Messages))
	}
	last := conv.Messages[len(conv.Messages)-1]
	if last.Role != openai.RoleAssistant || last.Content.Text != "respuesta2" {
		t.Errorf("expected last message to be the second reply, got %#v", last)
	}
	prev := conv.Messages[len(conv.Messages)-2]
	if prev.Role != openai.RoleUser || prev.Content.Text != "segunda pregunta" {
		t.Errorf("expected second user message, got %#v", prev)
	}
}

func TestConversationsCRUD(t *testing.T) {
	handler, store := newTestServerWithConversations(t, fakellm.New())

	// Lista vacía.
	rec := doJSON(t, handler, http.MethodGet, "/api/conversations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	payload := decodeResponse[struct {
		Conversations []conversationstore.Summary `json:"conversations"`
	}](t, rec)
	if len(payload.Conversations) != 0 {
		t.Errorf("expected empty list, got %d", len(payload.Conversations))
	}

	// Seed de una conversación.
	if _, err := store.Append(reqCtx(), testConversationID, "test-model", []openai.Message{{
		Role:    openai.RoleUser,
		Content: openai.NewTextContent("hola"),
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec = doJSON(t, handler, http.MethodGet, "/api/conversations", "")
	payload = decodeResponse[struct {
		Conversations []conversationstore.Summary `json:"conversations"`
	}](t, rec)
	if len(payload.Conversations) != 1 || payload.Conversations[0].ID != testConversationID {
		t.Errorf("expected 1 conversation, got %#v", payload.Conversations)
	}

	// GET por id.
	rec = doJSON(t, handler, http.MethodGet, "/api/conversations/"+testConversationID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// GET inválido (id demasiado corto / con caracteres ilegales) → 400.
	rec = doJSON(t, handler, http.MethodGet, "/api/conversations/a", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid id, got %d", rec.Code)
	}
	rec = doJSON(t, handler, http.MethodGet, "/api/conversations/id%20con%20espacios", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid id with spaces, got %d", rec.Code)
	}

	// Rename.
	rec = doJSON(t, handler, http.MethodPatch, "/api/conversations/"+testConversationID, `{"title":"Nuevo título"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on rename, got %d: %s", rec.Code, rec.Body.String())
	}
	renamed := decodeResponse[struct {
		Conversation conversationstore.Conversation `json:"conversation"`
	}](t, rec)
	if renamed.Conversation.Title != "Nuevo título" {
		t.Errorf("expected renamed title, got %q", renamed.Conversation.Title)
	}

	// Delete.
	rec = doJSON(t, handler, http.MethodDelete, "/api/conversations/"+testConversationID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	rec = doJSON(t, handler, http.MethodGet, "/api/conversations/"+testConversationID, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", rec.Code)
	}
}

func TestConversationsUnavailableWithoutStore(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := doJSON(t, handler, http.MethodGet, "/api/conversations", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 without store, got %d", rec.Code)
	}
}

func TestConversationRecordingSkipsWithoutHeader(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"leaf"}, nil).
		StreamResponse([]string{"respuesta"}, nil)
	handler, store := newTestServerWithConversations(t, llm)

	rec := doJSON(t, handler, http.MethodPost, "/v1/chat/completions", chatRequestBody("sin header", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.List(reqCtx())) != 0 {
		t.Errorf("no conversation should be recorded without X-Conversation-ID")
	}
}
