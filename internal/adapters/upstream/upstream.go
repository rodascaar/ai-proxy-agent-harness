// Package upstream implementa el puerto ports.LLMClient contra cualquier
// upstream compatible con la API de chat completions de OpenAI (Ollama, LM
// Studio, llama.cpp, vLLM o cualquier API remota OpenAI-compatible).
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

// New construye el cliente. El http.Client compartido usa un Transport con
// keep-alive y pooling de conexiones afinado para reutilizar sockets TCP
// contra el upstream (el modelo queda cargado en el servidor; aquí solo
// importa no abrir una conexión nueva por cada fase).
func New(baseURL, apiKey string, timeout time.Duration) *Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: timeout, Transport: transport},
	}
}

// chatURL devuelve el endpoint de chat completions del upstream. La base es
// la raíz del API (p. ej. "http://127.0.0.1:11434/v1" o
// "https://generativelanguage.googleapis.com/v1beta/openai"), por lo que solo
// se añade "/chat/completions" (nunca "/v1/...", que duplicaría el prefijo).
func (c *Client) chatURL() string {
	return c.baseURL + "/chat/completions"
}

// Probe verifica que el upstream esté disponible consultando GET /models.
// Se usa como warmup opcional al arrancar (sin disparar una inferencia).
func (c *Client) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("building probe request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("probing upstream: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("upstream probe status %d", resp.StatusCode)
	}
	return nil
}

// ListModels implementa ports.ModelLister: consulta GET /models del
// upstream y devuelve los descriptores que anuncia. Se usa para poblar la UI
// con los modelos realmente disponibles (así el cliente no pide uno inexistente,
// lo que haría al upstream cargar/recargar un modelo de más).
func (c *Client) ListModels(ctx context.Context) ([]openai.ModelDescriptor, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("building list request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.doList(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var list struct {
		Data []openai.ModelDescriptor `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("decoding upstream models: %w", err)
	}
	return list.Data, nil
}

// doList ejecuta la petición GET al listado de modelos; reusa el manejador de
// errores tipado (*Error) del cliente.
func (c *Client) doList(ctx context.Context, req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return nil, &Error{Status: resp.StatusCode, Body: readErr.Error()}
		}
		return nil, &Error{Status: resp.StatusCode, Body: string(raw)}
	}
	return resp, nil
}

// jsonSchemaFormat es el formato response_format.type=json_schema del upstream.
type jsonSchemaFormat struct {
	Type       string             `json:"type"`
	JSONSchema *jsonSchemaWrapper `json:"json_schema,omitempty"`
}

type jsonSchemaWrapper struct {
	Name   string      `json:"name"`
	Schema interface{} `json:"schema"`
	Strict bool        `json:"strict"`
}

// decompositionSchema es el JSON Schema para la salida de descomposición:
// {"atomic": boolean, "subtasks": string[]}
var decompositionSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"atomic":   map[string]string{"type": "boolean"},
		"subtasks": map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
	},
	"required":             []string{"atomic", "subtasks"},
	"additionalProperties": false,
}

// chatPayload es el cuerpo del POST al upstream. Stream se serializa
// explícitamente (true/false) para no depender de defaults ajenos. Los campos
// de sampling (temperature, max_tokens, stop) son opcionales: solo se envían
// cuando el motor los pide, para no pisar los defaults del servidor.
type chatPayload struct {
	Model          string            `json:"model"`
	Messages       []openai.Message  `json:"messages"`
	Stream         bool              `json:"stream"`
	ResponseFormat *jsonSchemaFormat `json:"response_format,omitempty"`
	Tools          []openai.Tool     `json:"tools,omitempty"`
	ToolChoice     json.RawMessage   `json:"tool_choice,omitempty"`
	Temperature    *float64          `json:"temperature,omitempty"`
	MaxTokens      *int              `json:"max_tokens,omitempty"`
	Stop           []string          `json:"stop,omitempty"`
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

// applySampling copia los campos de sampling opcionales al payload cuando el
// motor los pide. Se usa en Complete y Stream para no duplicar la asignación.
func applySampling(payload *chatPayload, temperature *float64, maxTokens *int, stop []string) {
	payload.Temperature = temperature
	payload.MaxTokens = maxTokens
	payload.Stop = stop
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
	applySampling(&payload, req.Temperature, req.MaxTokens, req.Stop)
	if req.JSONMode {
		payload.ResponseFormat = &jsonSchemaFormat{
			Type: "json_schema",
			JSONSchema: &jsonSchemaWrapper{
				Name:   "decomposition",
				Schema: decompositionSchema,
				Strict: true,
			},
		}
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
	applySampling(&payload, req.Temperature, req.MaxTokens, req.Stop)

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
