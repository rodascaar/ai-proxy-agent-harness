// Package fakellm provee una implementación falsa de ports.LLMClient para
// tests: una cola de respuestas programadas (no-streaming, streaming, error)
// y el registro de todas las requests recibidas para inspección.
package fakellm

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
)

// response es una respuesta programada en la cola.
type response struct {
	kind      string
	content   string
	pieces    []string
	toolCalls []openai.ToolCall
	err       error
}

// RecordedRequest es el payload de una llamada al fake, para inspección en
// los tests.
type RecordedRequest struct {
	Stream     bool
	Messages   []openai.Message
	JSONMode   bool
	Tools      []openai.Tool
	ToolChoice json.RawMessage
}

// Fake implementa ports.LLMClient con respuestas programadas FIFO.
type Fake struct {
	mu       sync.Mutex
	queue    []response
	received []RecordedRequest
}

// New construye un fake vacío.
func New() *Fake {
	return &Fake{}
}

// Completion encola una respuesta no-streaming.
func (f *Fake) Completion(content string) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, response{kind: "completion", content: content})
	return f
}

// StreamResponse encola una respuesta streaming: piezas de texto y/o tool
// calls.
func (f *Fake) StreamResponse(pieces []string, toolCalls []openai.ToolCall) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, response{kind: "stream", pieces: pieces, toolCalls: toolCalls})
	return f
}

// Error encola un error que la siguiente llamada devuelve tal cual.
func (f *Fake) Error(err error) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, response{kind: "error", err: err})
	return f
}

// Complete devuelve la siguiente respuesta no-streaming programada.
func (f *Fake) Complete(ctx context.Context, req ports.CompleteRequest) (string, error) {
	f.mu.Lock()
	f.record(RecordedRequest{
		Messages:   req.Messages,
		JSONMode:   req.JSONMode,
		Tools:      req.Tools,
		ToolChoice: req.ToolChoice,
	})
	resp, err := f.popLocked()
	f.mu.Unlock()
	if err != nil {
		return "", err
	}
	if resp.kind == "error" {
		return "", resp.err
	}
	if resp.kind != "completion" {
		return "", errors.New("fakellm: expected a completion response")
	}
	return resp.content, nil
}

// Stream emite la siguiente respuesta streaming programada, delta a delta.
func (f *Fake) Stream(ctx context.Context, req ports.StreamRequest, onChunk func(ports.StreamChunk) error) error {
	f.mu.Lock()
	f.record(RecordedRequest{
		Stream:     true,
		Messages:   req.Messages,
		Tools:      req.Tools,
		ToolChoice: req.ToolChoice,
	})
	resp, err := f.popLocked()
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if resp.kind == "error" {
		return resp.err
	}
	if resp.kind != "stream" {
		return errors.New("fakellm: expected a stream response")
	}
	for _, piece := range resp.pieces {
		content := piece
		if err := onChunk(ports.StreamChunk{Delta: openai.Delta{Content: &content}}); err != nil {
			return err
		}
	}
	for index, toolCall := range resp.toolCalls {
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

// Count devuelve cuántas llamadas recibió el fake.
func (f *Fake) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

// RecordAt devuelve la request registrada en un índice.
func (f *Fake) RecordAt(index int) RecordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.received[index]
}

// All devuelve todas las requests registradas.
func (f *Fake) All() []RecordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RecordedRequest{}, f.received...)
}

func (f *Fake) record(request RecordedRequest) {
	f.received = append(f.received, request)
}

func (f *Fake) popLocked() (response, error) {
	if len(f.queue) == 0 {
		return response{}, errors.New("fakellm: no more responses programmed")
	}
	resp := f.queue[0]
	f.queue = f.queue[1:]
	return resp, nil
}
