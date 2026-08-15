package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
)

func TestCompleteNonStreaming(t *testing.T) {
	var received chatPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("expected Authorization header, got %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatalf("decoding payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hola"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "sk-test", 5*time.Second)
	out, err := client.Complete(context.Background(), ports.CompleteRequest{
		Model:    "m",
		Messages: []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("hi")}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if out != "hola" {
		t.Errorf("expected content %q, got %q", "hola", out)
	}
	if received.Stream {
		t.Errorf("expected stream=false in payload")
	}
	if received.Model != "m" || len(received.Messages) != 1 {
		t.Errorf("unexpected payload: %#v", received)
	}
}

func TestCompleteJSONMode(t *testing.T) {
	var received chatPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		_, _ = io.WriteString(w, `{"id":"1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"{}"}}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "", 5*time.Second)
	if _, err := client.Complete(context.Background(), ports.CompleteRequest{
		Model:    "m",
		Messages: []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("x")}},
		JSONMode: true,
	}); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if received.ResponseFormat == nil || received.ResponseFormat.Type != "json_schema" || received.ResponseFormat.JSONSchema == nil || received.ResponseFormat.JSONSchema.Name != "decomposition" {
		t.Errorf("expected response_format json_schema with name=decomposition, got %#v", received.ResponseFormat)
	}
}

func TestCompleteWithoutAPIKeyOmitsAuthorization(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"id":"1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":""}}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "", 5*time.Second)
	if _, err := client.Complete(context.Background(), ports.CompleteRequest{
		Model:    "m",
		Messages: []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("x")}},
	}); err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header, got %q", gotAuth)
	}
}

func TestCompleteToolChoicePassthrough(t *testing.T) {
	var received chatPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		_, _ = io.WriteString(w, `{"id":"1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "", 5*time.Second)
	_, err := client.Complete(context.Background(), ports.CompleteRequest{
		Model:      "m",
		Messages:   []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("x")}},
		ToolChoice: json.RawMessage(`"auto"`),
		Tools:      []openai.Tool{{Type: "function", Function: openai.FunctionDef{Name: "f"}}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if string(received.ToolChoice) != `"auto"` {
		t.Errorf("expected tool_choice passthrough, got %s", received.ToolChoice)
	}
	if len(received.Tools) != 1 || received.Tools[0].Function.Name != "f" {
		t.Errorf("expected tools passthrough, got %#v", received.Tools)
	}
}

func TestCompleteUpstreamErrorIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	defer server.Close()

	client := New(server.URL, "sk", 5*time.Second)
	_, err := client.Complete(context.Background(), ports.CompleteRequest{
		Model:    "m",
		Messages: []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("x")}},
	})
	var upErr *Error
	if !errors.As(err, &upErr) {
		t.Fatalf("expected *upstream.Error, got %T: %v", err, err)
	}
	if upErr.Status != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", upErr.Status)
	}
	if !strings.Contains(upErr.Body, "boom") {
		t.Errorf("expected body to contain the upstream message, got %q", upErr.Body)
	}
}

func TestStreamParsesDeltasAndFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hola\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"pienso\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "sk", 5*time.Second)
	var contents []string
	var reasoning []string
	var finishReasons []string
	err := client.Stream(context.Background(), ports.StreamRequest{
		Model:    "m",
		Messages: []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("x")}},
	}, func(chunk ports.StreamChunk) error {
		if chunk.Delta.Content != nil {
			contents = append(contents, *chunk.Delta.Content)
		}
		if chunk.Delta.ReasoningContent != nil {
			reasoning = append(reasoning, *chunk.Delta.ReasoningContent)
		}
		if chunk.FinishReason != nil {
			finishReasons = append(finishReasons, *chunk.FinishReason)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	if len(contents) != 1 || contents[0] != "hola" {
		t.Errorf("expected content deltas [hola], got %#v", contents)
	}
	if len(reasoning) != 1 || reasoning[0] != "pienso" {
		t.Errorf("expected reasoning deltas [pienso], got %#v", reasoning)
	}
	if len(finishReasons) != 1 || finishReasons[0] != "stop" {
		t.Errorf("expected finish_reason [stop], got %#v", finishReasons)
	}
}

func TestStreamParsesToolCallDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"leer\",\"arguments\":\"{}\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "sk", 5*time.Second)
	var calls []openai.ToolCallDelta
	err := client.Stream(context.Background(), ports.StreamRequest{
		Model:    "m",
		Messages: []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("x")}},
	}, func(chunk ports.StreamChunk) error {
		calls = append(calls, chunk.Delta.ToolCalls...)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	if len(calls) != 1 || calls[0].ID == nil || *calls[0].ID != "call_1" {
		t.Fatalf("expected tool call delta, got %#v", calls)
	}
}

func TestStreamPropagatesOnChunkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hola\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"mundo\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "sk", 5*time.Second)
	stopErr := errors.New("client disconnect")
	err := client.Stream(context.Background(), ports.StreamRequest{
		Model:    "m",
		Messages: []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("x")}},
	}, func(chunk ports.StreamChunk) error {
		if chunk.Delta.Content != nil && *chunk.Delta.Content == "mundo" {
			return stopErr
		}
		return nil
	})
	if !errors.Is(err, stopErr) {
		t.Fatalf("expected onChunk error to propagate, got %v", err)
	}
}

func TestStreamSkipsMalformedSSELines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keep-alive comment\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"solo\"}}]}\n\n")
		_, _ = io.WriteString(w, "\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "sk", 5*time.Second)
	var contents []string
	err := client.Stream(context.Background(), ports.StreamRequest{
		Model:    "m",
		Messages: []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("x")}},
	}, func(chunk ports.StreamChunk) error {
		if chunk.Delta.Content != nil {
			contents = append(contents, *chunk.Delta.Content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}
	if len(contents) != 1 || contents[0] != "solo" {
		t.Errorf("expected only the real delta, got %#v", contents)
	}
}

func TestStreamRespectsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := New(server.URL, "sk", 10*time.Second)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Stream(ctx, ports.StreamRequest{
			Model:    "m",
			Messages: []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("x")}},
		}, func(ports.StreamChunk) error { return nil })
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("expected an error after cancellation, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Stream() did not return after context cancellation")
	}
}

func TestListModelsParsesUpstreamList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("expected Authorization header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"qwen2.5:7b","object":"model","owned_by":"ollama"}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "sk-test", 5*time.Second)
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "qwen2.5:7b" || models[0].OwnedBy != "ollama" {
		t.Errorf("unexpected models: %#v", models)
	}
}

func TestListModelsPropagatesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"no upstream"}`)
	}))
	defer server.Close()

	client := New(server.URL, "", 5*time.Second)
	_, err := client.ListModels(context.Background())
	var upErr *Error
	if !errors.As(err, &upErr) {
		t.Fatalf("expected *upstream.Error, got %T: %v", err, err)
	}
	if upErr.Status != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", upErr.Status)
	}
}

func TestProbeReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer server.Close()

	client := New(server.URL, "", 5*time.Second)
	if err := client.Probe(context.Background()); err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
}

func TestProbeUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := New(server.URL, "", 5*time.Second)
	if err := client.Probe(context.Background()); err == nil {
		t.Fatalf("expected error when upstream unavailable")
	}
}
