package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
)

const modelListTTL = 30 * time.Second

// modelCache cachea brevemente el listado de modelos del upstream para no
// disparar GET /v1/models en cada request del cliente (el listado cambia con
// poca frecuencia). Si lister es nil, devuelve vacío (se expone solo el
// modelo por defecto).
type modelCache struct {
	lister  ports.ModelLister
	mu      sync.Mutex
	cached  []openai.ModelDescriptor
	fetched time.Time
}

func newModelCache(lister ports.ModelLister) *modelCache {
	return &modelCache{lister: lister}
}

func (m *modelCache) list(ctx context.Context) []openai.ModelDescriptor {
	if m.lister == nil {
		return nil
	}
	m.mu.Lock()
	if m.cached != nil && time.Since(m.fetched) < modelListTTL {
		cached := m.cached
		m.mu.Unlock()
		return cached
	}
	m.mu.Unlock()

	fetched, err := m.lister.ListModels(ctx)
	if err != nil {
		// Si el upstream no responde, no rompemos GET /v1/models: devolvemos
		// lo cacheado (aunque sea nil) y seguimos exponiendo el default.
		m.mu.Lock()
		cached := m.cached
		m.mu.Unlock()
		return cached
	}
	m.mu.Lock()
	m.cached = fetched
	m.fetched = time.Now()
	m.mu.Unlock()
	return fetched
}

// models expone GET /v1/models: pasa los modelos anunciados por el upstream
// (cacheado) y asegura que el modelo por defecto esté siempre presente.
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	upstreamModels := s.modelCache.list(ctx)
	data := make([]openai.ModelDescriptor, 0, len(upstreamModels)+1)
	seen := make(map[string]bool, len(upstreamModels)+1)

	for _, m := range upstreamModels {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if m.Object == "" {
			m.Object = openai.ObjectModel
		}
		if m.OwnedBy == "" {
			m.OwnedBy = "upstream"
		}
		data = append(data, m)
	}

	if !seen[s.defaultModel] {
		data = append(data, openai.ModelDescriptor{
			ID:      s.defaultModel,
			Object:  openai.ObjectModel,
			OwnedBy: "atomic-proxy",
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"object": openai.ObjectList,
		"data":   data,
	})
}
