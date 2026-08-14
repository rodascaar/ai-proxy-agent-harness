// Package upstream implementa el puerto ports.LLMClient contra cualquier
// upstream compatible con la API de chat completions de OpenAI (Ollama, LM
// Studio, llama.cpp, vLLM, DeepSeek, etc.).
package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
)

// Error es un error tipado de upstream con el código HTTP y el cuerpo crudo,
// para que la capa HTTP pueda mapearlo a un 502 upstream_error.
type Error struct {
	Status int
	Body   string
}

// Error implementa error.
func (e *Error) Error() string {
	return fmt.Sprintf("upstream error %d: %s", e.Status, e.Body)
}

// Client implementa ports.LLMClient.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New construye el cliente. El http.Client compartido (pooling de conexiones)
// se crea con el timeout de la configuración. Un apiKey vacío omite el header
// Authorization.
func New(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: timeout},
	}
}

// chatURL devuelve el endpoint de chat completions del upstream.
func (c *Client) chatURL() string {
	return c.baseURL + "/v1/chat/completions"
}

// jsonFormat habilita el modo JSON del upstream.
type jsonFormat struct {
	Type string `json:"type"`
}

// chatPayload es el cuerpo del POST al upstream. Stream se serializa
// explícitamente (true/false) para no depender de defaults ajenos.
type chatPayload struct {
	Model          string           `json:"model"`
	Messages       []openai.Message `json:"messages"`
	Stream         bool             `json:"stream"`
	ResponseFormat *jsonFormat      `json:"response_format,omitempty"`
	Tools          []openai.Tool    `json:"tools,omitempty"`
	ToolChoice     json.RawMessage  `json:"tool_choice,omitempty"`
}

// do ejecuta una petición POST y devuelve la respuesta ya verificada (2xx).
// Los errores de transporte respetan la cancelación del contexto; los errores
// >=400 se tipan como *Error.
func (c *Client) do(ctx context.Context, payload chatPayload) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling upstream request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return nil, fmt.Errorf("reading upstream error body: %w", readErr)
		}
		return nil, &Error{Status: resp.StatusCode, Body: string(raw)}
	}
	return resp, nil
}

// Complete hace una llamada no-streaming y devuelve el contenido de texto de
// la respuesta (o "" si el mensaje no trae content).
func (c *Client) Complete(ctx context.Context, req ports.CompleteRequest) (string, error) {
	payload := chatPayload{
		Model:      req.Model,
		Messages:   req.Messages,
		Stream:     false,
		Tools:      req.Tools,
		ToolChoice: req.ToolChoice,
	}
	if req.JSONMode {
		payload.ResponseFormat = &jsonFormat{Type: "json_object"}
	}

	resp, err := c.do(ctx, payload)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decoding upstream response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("upstream response without choices")
	}
	content := parsed.Choices[0].Message.Content
	if content == nil {
		return "", nil
	}
	return *content, nil
}

// Stream hace una llamada streaming e invoca onChunk por cada delta. Parseo
// SSE de las líneas "data:", terminando en "[DONE]". Devuelve el primer error
// que onChunk produzca o un error de transporte; respeta el contexto.
func (c *Client) Stream(ctx context.Context, req ports.StreamRequest, onChunk func(ports.StreamChunk) error) error {
	payload := chatPayload{
		Model:      req.Model,
		Messages:   req.Messages,
		Stream:     true,
		Tools:      req.Tools,
		ToolChoice: req.ToolChoice,
	}

	resp, err := c.do(ctx, payload)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			return nil
		}
		var chunk openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("parsing upstream stream chunk: %w", err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if err := onChunk(ports.StreamChunk{Delta: choice.Delta, FinishReason: choice.FinishReason}); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading upstream stream: %w", err)
	}
	return nil
}
