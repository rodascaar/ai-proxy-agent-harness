// Package ports define las interfaces que el dominio necesita y que los
// adaptadores de infraestructura implementan (hexagonal: puertos al mundo
// exterior).
package ports

import (
	"context"
	"encoding/json"

	"ai-proxy-agent-harness/internal/core/openai"
)

// CompleteRequest describe una llamada no-streaming al upstream.
type CompleteRequest struct {
	Model      string
	Messages   []openai.Message
	JSONMode   bool
	Tools      []openai.Tool
	ToolChoice json.RawMessage
}

// StreamRequest describe una llamada streaming al upstream.
type StreamRequest struct {
	Model      string
	Messages   []openai.Message
	Tools      []openai.Tool
	ToolChoice json.RawMessage
}

// StreamChunk es un fragmento de respuesta del upstream durante el streaming.
type StreamChunk struct {
	Delta        openai.Delta
	FinishReason *string
}

// LLMClient es el puerto hacia cualquier upstream compatible con la API de
// chat completions de OpenAI. Los adaptadores que lo implementan aíslan el
// transporte HTTP de la lógica del motor.
type LLMClient interface {
	// Complete hace una llamada no-streaming y devuelve el contenido de texto
	// de la respuesta.
	Complete(ctx context.Context, req CompleteRequest) (string, error)
	// Stream hace una llamada streaming e invoca onChunk por cada delta. Debe
	// devolver el primer error que onChunk produzca o un error de transporte,
	// y debe respetar la cancelación del contexto.
	Stream(ctx context.Context, req StreamRequest, onChunk func(StreamChunk) error) error
}

// ModelLister es el puerto opcional para consultar los modelos que expone el
// upstream via GET /v1/models. Sirve a la UI para que el cliente elija un
// modelo que realmente exista en el upstream (y así reutilizar el modelo ya
// cargado en vez de disparar una recarga).
type ModelLister interface {
	// ListModels devuelve los modelos que el upstream anuncia, en orden.
	ListModels(ctx context.Context) ([]openai.ModelDescriptor, error)
}

// LLMRouter enruta llamadas a múltiples upstreams por nombre de modelo. Es la
// base del debate multi-modelo: con varios upstreams (locales y remotos) el
// proxy puede pedirle a un modelo que critique el resultado de otro. Si solo
// hay un modelo, ese mismo cubre todos los roles.
type LLMRouter interface {
	LLMClient
	ModelLister

	// ClientFor devuelve el cliente que sirve a un modelo concreto. Si el
	// modelo no está declarado en ningún upstream, devuelve el cliente del
	// primer upstream (comportamiento legado de passthrough).
	ClientFor(model string) (LLMClient, error)

	// AllModels devuelve los modelos disponibles en orden, sin duplicados.
	AllModels() []string

	// Probe verifica la conectividad con todos los upstreams (warmup).
	Probe(ctx context.Context) error
}
