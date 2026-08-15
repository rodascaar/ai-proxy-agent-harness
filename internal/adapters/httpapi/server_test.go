package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-proxy-agent-harness/internal/adapters/httpapi"
	"ai-proxy-agent-harness/internal/adapters/sessionstore/md"
	"ai-proxy-agent-harness/internal/adapters/upstream"
	"ai-proxy-agent-harness/internal/application/service"
	"ai-proxy-agent-harness/internal/config"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/testutil/fakellm"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t *testing.T, llm *fakellm.Fake, exposeReasoning bool) (http.Handler, *md.Store) {
	t.Helper()
	store, err := md.New(t.TempDir(), time.Minute, 100, noopLogger())
	if err != nil {
		t.Fatalf("md.New() error: %v", err)
	}
	svc := service.New(llm, store, "test-model", 3, 25, noopLogger())
	cfg := &config.Config{
		UpstreamBaseURL:        "http://localhost:11434/v1",
		UpstreamModel:          "test-model",
		MaxDecompositionDepth:  3,
		MaxToolRoundsPerPhase:  25,
		ProxyPort:              8000,
		RequestTimeout:         time.Minute,
		SessionTTL:             time.Minute,
		MaxSessions:            100,
		ExposeReasoningContent: exposeReasoning,
		SessionsDir:            ".sessions",
	}
	return httpapi.New(svc, cfg, nil, noopLogger()), store
}

func doJSON(t *testing.T, handler http.Handler, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeResponse[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
	return out
}

func sseData(body string) []string {
	var out []string
	for _, event := range strings.Split(body, "\n\n") {
		line := strings.TrimSpace(event)
		if strings.HasPrefix(line, "data:") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return out
}

func decodeChunk(t *testing.T, data string) openai.ChatCompletionChunk {
	t.Helper()
	var chunk openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		t.Fatalf("decoding chunk %q: %v", data, err)
	}
	return chunk
}

func atomicCompletion() string {
	return `{"atomic": true, "subtasks": []}`
}

func TestHealthz(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := doJSON(t, handler, "GET", "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", rec.Code)
	}
}

func TestRequestID(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)

	rec := doJSON(t, handler, "GET", "/healthz", "")
	if rec.Header().Get("X-Request-ID") == "" {
		t.Errorf("generated request id header missing")
	}

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("X-Request-ID", "req-123")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-ID"); got != "req-123" {
		t.Errorf("X-Request-ID = %q, want echoed req-123", got)
	}
}

func TestModels(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := doJSON(t, handler, http.MethodGet, "/v1/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	payload := decodeResponse[struct {
		Data []openai.ModelDescriptor `json:"data"`
	}](t, rec)
	if len(payload.Data) != 1 || payload.Data[0].ID != "test-model" {
		t.Errorf("unexpected models payload %#v", payload.Data)
	}
}

func TestNonStreamingBasicFlow(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"resultado hoja"}, nil).
		StreamResponse([]string{"respuesta final"}, nil)
	handler, store := newTestServer(t, llm, true)

	rec := doJSON(t, handler, http.MethodPost, "/v1/chat/completions",
		`{"model":"test-model","messages":[{"role":"user","content":"haz algo simple"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := decodeResponse[openai.ChatCompletionResponse](t, rec)
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice")
	}
	choice := resp.Choices[0]
	if choice.Message.Content == nil || *choice.Message.Content != "respuesta final" {
		t.Errorf("unexpected content %v", choice.Message.Content)
	}
	if choice.FinishReason == nil || *choice.FinishReason != "stop" {
		t.Errorf("expected finish_reason stop, got %v", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 0 {
		t.Errorf("unexpected tool_calls in completed run")
	}

	// La sesión debe quedar persistida.
	messages := []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("haz algo simple")}}
	state, err := store.FindMatching(reqCtx(), messages)
	if err != nil || state == nil {
		t.Fatalf("expected persisted session, err=%v", err)
	}
	if state.PendingPhase != 0 {
		t.Errorf("expected no pending phase, got %v", state.PendingPhase)
	}
}

func TestNonStreamingToolCallsThenResume(t *testing.T) {
	toolCall := openai.ToolCall{ID: "call_1", Type: "function", Function: openai.FunctionCall{Name: "leer", Arguments: "{}"}}
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse(nil, []openai.ToolCall{toolCall}).
		StreamResponse([]string{"listo"}, nil).
		StreamResponse([]string{"final"}, nil)
	handler, _ := newTestServer(t, llm, true)

	req1Body := `{"model":"test-model","messages":[{"role":"user","content":"usa la herramienta"}]}`
	rec1 := doJSON(t, handler, http.MethodPost, "/v1/chat/completions", req1Body)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 on request 1, got %d: %s", rec1.Code, rec1.Body.String())
	}
	resp1 := decodeResponse[openai.ChatCompletionResponse](t, rec1)
	if resp1.Choices[0].FinishReason == nil || *resp1.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %v", resp1.Choices[0].FinishReason)
	}
	if len(resp1.Choices[0].Message.ToolCalls) != 1 || resp1.Choices[0].Message.ToolCalls[0].ID != "call_1" {
		t.Fatalf("expected one tool call call_1, got %#v", resp1.Choices[0].Message.ToolCalls)
	}

	// Request 2: resume con el resultado de la tool.
	req2Body := `{"model":"test-model","messages":[
		{"role":"user","content":"usa la herramienta"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"leer","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"contenido leído"}
	]}`
	rec2 := doJSON(t, handler, http.MethodPost, "/v1/chat/completions", req2Body)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on request 2, got %d: %s", rec2.Code, rec2.Body.String())
	}
	resp2 := decodeResponse[openai.ChatCompletionResponse](t, rec2)
	if resp2.Choices[0].Message.Content == nil || *resp2.Choices[0].Message.Content != "final" {
		t.Errorf("expected final content, got %v", resp2.Choices[0].Message.Content)
	}
	if resp2.Choices[0].FinishReason == nil || *resp2.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason stop on resume, got %v", resp2.Choices[0].FinishReason)
	}

	// El resume NO debe redecomponer: 1 completion (req1) + 2 streams (req1
	// hoja, req2 hoja) + 1 stream (req2 síntesis) = 4 llamadas, y la llamada 2
	// (primer stream del resume) debe traer el output de la tool.
	if llm.Count() != 4 {
		t.Errorf("expected 4 upstream calls total, got %d", llm.Count())
	}
	resumeStream := llm.RecordAt(2)
	if !resumeStream.Stream {
		t.Errorf("expected resume to start with a stream, not a completion")
	}
	if !messagesContainToolResult(resumeStream.Messages, "call_1", "contenido leído") {
		t.Errorf("resume stream should carry the tool result for call_1")
	}
}

func messagesContainToolResult(messages []openai.Message, id, content string) bool {
	for _, message := range messages {
		if message.Role == openai.RoleTool && message.ToolCallID != nil && *message.ToolCallID == id {
			if message.Content != nil && message.Content.Text == content {
				return true
			}
		}
	}
	return false
}

func TestNewTurnSeedsPriorContext(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"r1"}, nil).
		StreamResponse([]string{"final1"}, nil).
		Completion(atomicCompletion()).
		StreamResponse([]string{"r2"}, nil).
		StreamResponse([]string{"final2"}, nil)
	handler, _ := newTestServer(t, llm, true)

	rec1 := doJSON(t, handler, http.MethodPost, "/v1/chat/completions",
		`{"model":"test-model","messages":[{"role":"user","content":"primera pregunta"}]}`)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 on request 1, got %d", rec1.Code)
	}

	// Mismo prefijo + mensaje nuevo del usuario = turno nuevo de la misma
	// conversación.
	rec2 := doJSON(t, handler, http.MethodPost, "/v1/chat/completions",
		`{"model":"test-model","messages":[
			{"role":"user","content":"primera pregunta"},
			{"role":"assistant","content":"final1"},
			{"role":"user","content":"segunda pregunta"}
		]}`)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on request 2, got %d: %s", rec2.Code, rec2.Body.String())
	}
	resp2 := decodeResponse[openai.ChatCompletionResponse](t, rec2)
	if resp2.Choices[0].Message.Content == nil || *resp2.Choices[0].Message.Content != "final2" {
		t.Errorf("expected final2 content, got %v", resp2.Choices[0].Message.Content)
	}

	// El turno 2 decompone SOLO el nuevo turno; la descomposición debe venir
	// sembrada con el turn_history (final1) como contexto previo.
	secondDecomposition := llm.RecordAt(3)
	if !secondDecomposition.JSONMode {
		t.Errorf("expected second decomposition in json mode")
	}
	userText := secondDecomposition.Messages[1].Content.Text
	if !strings.Contains(userText, "final1") {
		t.Errorf("expected prior context to contain final1, got %q", userText)
	}
}

func TestStreamingBasicFlow(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"hola"}, nil).
		StreamResponse([]string{" mundo"}, nil)
	handler, _ := newTestServer(t, llm, true)

	rec := doJSON(t, handler, http.MethodPost, "/v1/chat/completions",
		`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"saluda"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	data := sseData(rec.Body.String())
	if len(data) < 3 || data[len(data)-1] != "[DONE]" {
		t.Fatalf("expected stream ending with [DONE], got %#v", data)
	}

	var contents []string
	var reasoning []string
	var finishReasons []string
	roles := 0
	for _, item := range data[:len(data)-1] {
		chunk := decodeChunk(t, item)
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Role != nil && *delta.Role == openai.RoleAssistant {
			roles++
		}
		if delta.Content != nil {
			contents = append(contents, *delta.Content)
		}
		if delta.ReasoningContent != nil {
			reasoning = append(reasoning, *delta.ReasoningContent)
		}
		if chunk.Choices[0].FinishReason != nil {
			finishReasons = append(finishReasons, *chunk.Choices[0].FinishReason)
		}
	}
	if roles != 1 {
		t.Errorf("expected a leading role chunk, got %d", roles)
	}
	// El texto de la hoja viaja como reasoning; el content final es la
	// síntesis.
	if strings.Join(contents, "") != " mundo" {
		t.Errorf("expected content ' mundo', got %q", strings.Join(contents, ""))
	}
	if strings.Contains(strings.Join(reasoning, ""), "hola") == false {
		t.Errorf("expected leaf text 'hola' inside reasoning, got %q", strings.Join(reasoning, ""))
	}
	if len(finishReasons) != 1 || finishReasons[0] != "stop" {
		t.Errorf("expected final stop, got %#v", finishReasons)
	}
}

func TestStreamingWithToolCallsPersistsPause(t *testing.T) {
	toolCall := openai.ToolCall{ID: "call_1", Type: "function", Function: openai.FunctionCall{Name: "leer", Arguments: "{}"}}
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse(nil, []openai.ToolCall{toolCall})
	handler, store := newTestServer(t, llm, true)

	rec := doJSON(t, handler, http.MethodPost, "/v1/chat/completions",
		`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"usa la herramienta"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	data := sseData(rec.Body.String())
	if len(data) < 3 || data[len(data)-1] != "[DONE]" {
		t.Fatalf("expected stream ending with [DONE], got %#v", data)
	}
	var sawToolCalls bool
	var finish string
	for _, item := range data[:len(data)-1] {
		chunk := decodeChunk(t, item)
		if len(chunk.Choices) == 0 {
			continue
		}
		if len(chunk.Choices[0].Delta.ToolCalls) > 0 {
			sawToolCalls = true
		}
		if chunk.Choices[0].FinishReason != nil {
			finish = *chunk.Choices[0].FinishReason
		}
	}
	if !sawToolCalls {
		t.Errorf("expected a tool_calls chunk in the stream")
	}
	if finish != "tool_calls" {
		t.Errorf("expected finish_reason tool_calls, got %q", finish)
	}

	// La pausa debe estar persistida.
	messages := []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("usa la herramienta")}}
	state, err := store.FindMatching(reqCtx(), messages)
	if err != nil || state == nil {
		t.Fatalf("expected persisted paused session, err=%v", err)
	}
	if len(state.PendingToolCalls) != 1 || state.PendingToolCalls[0].ID != "call_1" {
		t.Errorf("expected pending tool call call_1, got %#v", state.PendingToolCalls)
	}
}

func TestUpstreamErrorNonStreaming(t *testing.T) {
	llm := fakellm.New().Error(&upstream.Error{Status: http.StatusBadGateway, Body: `boom`})
	handler, _ := newTestServer(t, llm, true)

	rec := doJSON(t, handler, http.MethodPost, "/v1/chat/completions",
		`{"model":"test-model","messages":[{"role":"user","content":"x"}]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeResponse[openai.ErrorResponse](t, rec)
	if body.Error.Type != "upstream_error" {
		t.Errorf("expected type upstream_error, got %q", body.Error.Type)
	}
	if !strings.Contains(body.Error.Message, "boom") {
		t.Errorf("expected message to mention the upstream error, got %q", body.Error.Message)
	}
}

func TestUpstreamErrorStreaming(t *testing.T) {
	llm := fakellm.New().Error(&upstream.Error{Status: http.StatusBadGateway, Body: `boom`})
	handler, _ := newTestServer(t, llm, true)

	rec := doJSON(t, handler, http.MethodPost, "/v1/chat/completions",
		`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"x"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("SSE errors are delivered in-stream, expected 200, got %d", rec.Code)
	}
	data := sseData(rec.Body.String())
	if len(data) < 2 || data[len(data)-1] != "[DONE]" {
		t.Fatalf("expected error line + [DONE], got %#v", data)
	}
	if !strings.Contains(data[len(data)-2], "upstream_error") {
		t.Errorf("expected an upstream_error payload, got %q", data[len(data)-2])
	}
}

func TestReasoningNotExposedWhenDisabled(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"ok"}, nil).
		StreamResponse([]string{"final"}, nil)
	handler, _ := newTestServer(t, llm, false)

	rec := doJSON(t, handler, http.MethodPost, "/v1/chat/completions",
		`{"model":"test-model","messages":[{"role":"user","content":"haz algo"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	resp := decodeResponse[openai.ChatCompletionResponse](t, rec)
	if resp.Choices[0].Message.ReasoningContent != nil {
		t.Errorf("reasoning should not be exposed when disabled")
	}
}

func TestReasoningExposedWhenEnabled(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"ok"}, nil).
		StreamResponse([]string{"final"}, nil)
	handler, _ := newTestServer(t, llm, true)

	rec := doJSON(t, handler, http.MethodPost, "/v1/chat/completions",
		`{"model":"test-model","messages":[{"role":"user","content":"haz algo"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	resp := decodeResponse[openai.ChatCompletionResponse](t, rec)
	if resp.Choices[0].Message.ReasoningContent == nil {
		t.Errorf("reasoning should be exposed when enabled")
	}
}

func TestInvalidRequest(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := doJSON(t, handler, http.MethodPost, "/v1/chat/completions", `{"model":"test-model"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func reqCtx() context.Context {
	return context.Background()
}
