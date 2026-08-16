// Package router implementa el puerto ports.LLMRouter: enruta las llamadas a
// múltiples upstreams OpenAI-compatibles por nombre de modelo. Cada upstream
// declara los modelos que sirve (config.Upstream), y una llamada con
// req.Model=X se despacha al upstream que lo expone. Para el debate
// multi-modelo expone AllModels() y ClientFor(model).
//
// Compatibilidad legada: si un modelo no está declarado en ningún upstream,
// se despacha al primer upstream (comportamiento de passthrough original).
package router

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"ai-proxy-agent-harness/internal/adapters/upstream"
	"ai-proxy-agent-harness/internal/config"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
	"time"
)

// binding asocia un upstream con los modelos que sirve.
type binding struct {
	client  *upstream.Client
	baseURL string
	models  map[string]bool
}

// Router enruta por modelo al upstream correcto.
type Router struct {
	bindings  []binding
	byModel   map[string]*upstream.Client
	allModels []string
	defaultC  *upstream.Client
	timeout   time.Duration
	mu        sync.RWMutex
}

// New construye el Router desde la lista de upstreams resuelta por config.
// Al menos un upstream es obligatorio (garantizado por config.validate).
func New(upstreams []config.Upstream, timeout time.Duration) *Router {
	r := &Router{
		byModel: make(map[string]*upstream.Client),
		timeout: timeout,
	}
	for _, up := range upstreams {
		client := upstream.New(up.BaseURL, up.APIKey, timeout)
		b := binding{
			client:  client,
			baseURL: up.BaseURL,
			models:  make(map[string]bool),
		}
		for _, model := range up.Models {
			if b.models[model] {
				continue
			}
			b.models[model] = true
			r.byModel[model] = client
		}
		r.bindings = append(r.bindings, b)
		if r.defaultC == nil {
			r.defaultC = client
		}
	}
	r.allModels = r.collectAllModels()
	return r
}

// collectAllModels ordena los modelos de forma determinista (ordinal del
// primer upstream, luego alfabético).
func (r *Router) collectAllModels() []string {
	seen := make(map[string]bool)
	var models []string
	for _, b := range r.bindings {
		for model := range b.models {
			if !seen[model] {
				seen[model] = true
				models = append(models, model)
			}
		}
	}
	sort.Strings(models)
	return models
}

// ClientFor devuelve el cliente que sirve a un modelo. Si el modelo no está
// declarado, cae al primer upstream (passthrough legado).
func (r *Router) ClientFor(model string) (ports.LLMClient, error) {
	if client, ok := r.byModel[model]; ok {
		return client, nil
	}
	if r.defaultC == nil {
		return nil, fmt.Errorf("router: no upstreams configured")
	}
	return r.defaultC, nil
}

// AllModels devuelve los modelos disponibles en orden determinista.
func (r *Router) AllModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.allModels...)
}

// Complete despacha al upstream que sirve req.Model.
func (r *Router) Complete(ctx context.Context, req ports.CompleteRequest) (string, error) {
	client, err := r.ClientFor(req.Model)
	if err != nil {
		return "", err
	}
	return client.Complete(ctx, req)
}

// Stream despacha al upstream que sirve req.Model.
func (r *Router) Stream(ctx context.Context, req ports.StreamRequest, onChunk func(ports.StreamChunk) error) error {
	client, err := r.ClientFor(req.Model)
	if err != nil {
		return err
	}
	return client.Stream(ctx, req, onChunk)
}

// ListModels agrega los modelos anunciados por todos los upstreams,
// deduplicados por id y en orden alfabético.
func (r *Router) ListModels(ctx context.Context) ([]openai.ModelDescriptor, error) {
	seen := make(map[string]bool)
	var result []openai.ModelDescriptor
	for _, b := range r.bindings {
		models, err := b.client.ListModels(ctx)
		if err != nil {
			// Si un upstream no responde, seguimos con los demás.
			continue
		}
		for _, model := range models {
			if seen[model.ID] {
				continue
			}
			seen[model.ID] = true
			result = append(result, model)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// Probe verifica la conectividad con todos los upstreams. Devuelve un error
// si alguno no responde, para el warmup al arrancar.
func (r *Router) Probe(ctx context.Context) error {
	var failures []string
	for _, b := range r.bindings {
		if err := b.client.Probe(ctx); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", b.baseURL, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("router: %s", strings.Join(failures, "; "))
	}
	return nil
}
