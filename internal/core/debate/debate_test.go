package debate

import (
	"context"
	"strings"
	"sync"
	"testing"

	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
)

// fakeRouter implementa ports.LLMRouter con respuestas programadas por modelo.
type fakeRouter struct {
	mu      sync.Mutex
	clients map[string]*fakeClient
	models  []string
}

// fakeClient implementa ports.LLMClient con una cola de respuestas.
type fakeClient struct {
	mu        sync.Mutex
	responses []string
}

func (f *fakeClient) Complete(ctx context.Context, req ports.CompleteRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.responses) == 0 {
		return "", nil
	}
	out := f.responses[0]
	f.responses = f.responses[1:]
	return out, nil
}

func (f *fakeClient) Stream(ctx context.Context, req ports.StreamRequest, onChunk func(ports.StreamChunk) error) error {
	return nil
}

func (r *fakeRouter) Complete(ctx context.Context, req ports.CompleteRequest) (string, error) {
	c, err := r.ClientFor(req.Model)
	if err != nil {
		return "", err
	}
	return c.Complete(ctx, req)
}

func (r *fakeRouter) Stream(ctx context.Context, req ports.StreamRequest, onChunk func(ports.StreamChunk) error) error {
	c, err := r.ClientFor(req.Model)
	if err != nil {
		return err
	}
	return c.Stream(ctx, req, onChunk)
}

func (r *fakeRouter) ListModels(ctx context.Context) ([]openai.ModelDescriptor, error) {
	return nil, nil
}

func (r *fakeRouter) Probe(ctx context.Context) error { return nil }

func (r *fakeRouter) ClientFor(model string) (ports.LLMClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[model]; ok {
		return c, nil
	}
	for _, c := range r.clients {
		return c, nil
	}
	return &fakeClient{}, nil
}

func (r *fakeRouter) AllModels() []string {
	return r.models
}

func newFakeRouter(models []string, responses map[string][]string) *fakeRouter {
	r := &fakeRouter{
		clients: make(map[string]*fakeClient),
		models:  models,
	}
	for _, m := range models {
		r.clients[m] = &fakeClient{responses: responses[m]}
	}
	return r
}

func TestRefineSingleModelApproves(t *testing.T) {
	// Un solo modelo (self-refine): la crítica aprueba, no se refina.
	r := newFakeRouter([]string{"m1"}, map[string][]string{
		"m1": {ApprovedMarker},
	})
	d := New(r, "m1", 2)

	var reasoning strings.Builder
	got, err := d.Refine(context.Background(), "suma 1+1", "2", func(text string) error {
		reasoning.WriteString(text)
		return nil
	})
	if err != nil {
		t.Fatalf("Refine() error: %v", err)
	}
	if got != "2" {
		t.Errorf("expected unchanged result, got %q", got)
	}
	if !strings.Contains(reasoning.String(), "crítico m1") {
		t.Errorf("expected critic reasoning, got %q", reasoning.String())
	}
}

func TestRefineSingleModelRefines(t *testing.T) {
	// Self-refine: crítica lista problemas, luego el refinador corrige, y la
	// segunda crítica aprueba.
	r := newFakeRouter([]string{"m1"}, map[string][]string{
		"m1": {"falta punto final", "corregido con punto final.", ApprovedMarker},
	})
	d := New(r, "m1", 2)

	got, err := d.Refine(context.Background(), "escribe hola", "hola", nil)
	if err != nil {
		t.Fatalf("Refine() error: %v", err)
	}
	if got != "corregido con punto final." {
		t.Errorf("expected refined result, got %q", got)
	}
}

func TestRefineMultiModelUsesDifferentCritic(t *testing.T) {
	// Dos modelos: m1 refina, m2 critica y aprueba en la segunda ronda.
	r := newFakeRouter([]string{"m1", "m2"}, map[string][]string{
		"m2": {"revisa el tipo de retorno", ApprovedMarker},
		"m1": {"int sumar(int a, int b) { return a + b; }"},
	})
	d := New(r, "m1", 2)

	var reasoning strings.Builder
	got, err := d.Refine(context.Background(), "función suma", "sumar(a, b) { return a + b; }", func(text string) error {
		reasoning.WriteString(text)
		return nil
	})
	if err != nil {
		t.Fatalf("Refine() error: %v", err)
	}
	if got == "" {
		t.Errorf("expected refined result")
	}
	if !strings.Contains(reasoning.String(), "crítico m2") {
		t.Errorf("expected critic to be m2, got reasoning %q", reasoning.String())
	}
	if !strings.Contains(reasoning.String(), "refinador m1") {
		t.Errorf("expected refiner to be m1, got reasoning %q", reasoning.String())
	}
}

func TestRefineStopsAtMaxRounds(t *testing.T) {
	// Crítico nunca aprueba: debe refinar solo `rounds` veces.
	r := newFakeRouter([]string{"m1"}, map[string][]string{
		"m1": {"problema 1", "r1", "problema 2", "r2", "problema 3", "r3"},
	})
	d := New(r, "m1", 2)

	got, err := d.Refine(context.Background(), "tarea", "inicial", nil)
	if err != nil {
		t.Fatalf("Refine() error: %v", err)
	}
	if got != "r2" {
		t.Errorf("expected result after 2 rounds (r2), got %q", got)
	}
}
