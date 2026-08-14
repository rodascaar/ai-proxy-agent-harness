package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
)

// errStop es un error centinela para interrumpir la iteración de eventos.
var errStop = errors.New("engine test: stop")

// fakeResponse es una respuesta programada para el LLM fake.
type fakeResponse struct {
	kind      string // "completion" | "stream"
	content   string
	pieces    []string
	toolCalls []openai.ToolCall
}

// recordedRequest es el payload de una llamada al LLM fake, para inspección
// en los tests (análogo a `received` del FakeUpstream original).
type recordedRequest struct {
	stream     bool
	messages   []openai.Message
	jsonMode   bool
	tools      []openai.Tool
	toolChoice json.RawMessage
}

// fakeLLM implementa ports.LLMClient con una cola de respuestas programadas,
// como el FakeUpstream del proyecto original.
type fakeLLM struct {
	mu       sync.Mutex
	queue    []fakeResponse
	received []recordedRequest
}

func (f *fakeLLM) queueCompletion(content string) {
	f.queue = append(f.queue, fakeResponse{kind: "completion", content: content})
}

func (f *fakeLLM) queueStream(pieces []string, toolCalls []openai.ToolCall) {
	f.queue = append(f.queue, fakeResponse{kind: "stream", pieces: pieces, toolCalls: toolCalls})
}

func (f *fakeLLM) pop() (fakeResponse, error) {
	if len(f.queue) == 0 {
		return fakeResponse{}, errors.New("fakeLLM: no more responses programmed")
	}
	response := f.queue[0]
	f.queue = f.queue[1:]
	return response, nil
}

func (f *fakeLLM) record(request recordedRequest) {
	f.received = append(f.received, request)
}

// Complete devuelve la siguiente respuesta no-streaming programada.
func (f *fakeLLM) Complete(ctx context.Context, req ports.CompleteRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(recordedRequest{
		messages:   req.Messages,
		jsonMode:   req.JSONMode,
		tools:      req.Tools,
		toolChoice: req.ToolChoice,
	})
	response, err := f.pop()
	if err != nil {
		return "", err
	}
	if response.kind != "completion" {
		return "", errors.New("fakeLLM: expected completion response")
	}
	return response.content, nil
}

// Stream emite la siguiente respuesta streaming programada, delta a delta.
func (f *fakeLLM) Stream(ctx context.Context, req ports.StreamRequest, onChunk func(ports.StreamChunk) error) error {
	f.mu.Lock()
	f.record(recordedRequest{
		stream:     true,
		messages:   req.Messages,
		tools:      req.Tools,
		toolChoice: req.ToolChoice,
	})
	response, err := f.pop()
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if response.kind != "stream" {
		return errors.New("fakeLLM: expected stream response")
	}
	for _, piece := range response.pieces {
		content := piece
		if err := onChunk(ports.StreamChunk{Delta: openai.Delta{Content: &content}}); err != nil {
			return err
		}
	}
	for index, toolCall := range response.toolCalls {
		idx := index
		id := toolCall.ID
		name := toolCall.Function.Name
		arguments := toolCall.Function.Arguments
		delta := openai.ToolCallDelta{
			Index:    &idx,
			ID:       &id,
			Function: &openai.FunctionDelta{Name: &name, Arguments: &arguments},
		}
		if toolCall.Type != "" {
			callType := toolCall.Type
			delta.Type = &callType
		}
		if err := onChunk(ports.StreamChunk{Delta: openai.Delta{ToolCalls: []openai.ToolCallDelta{delta}}}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeLLM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func (f *fakeLLM) recordAt(index int) recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.received[index]
}
