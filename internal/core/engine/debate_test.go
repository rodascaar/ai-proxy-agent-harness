package engine

import (
	"context"
	"strings"
	"testing"

	"ai-proxy-agent-harness/internal/core/goal"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
)

// fakeRouter implementa ports.LLMRouter delegando en un fakeLLM y exponiendo
// una lista fija de modelos, para ejercitar el debate en el motor.
type fakeRouter struct {
	llm    *fakeLLM
	models []string
}

func (r *fakeRouter) Complete(ctx context.Context, req ports.CompleteRequest) (string, error) {
	return r.llm.Complete(ctx, req)
}

func (r *fakeRouter) Stream(ctx context.Context, req ports.StreamRequest, onChunk func(ports.StreamChunk) error) error {
	return r.llm.Stream(ctx, req, onChunk)
}

func (r *fakeRouter) ListModels(ctx context.Context) ([]openai.ModelDescriptor, error) {
	return nil, nil
}

func (r *fakeRouter) ClientFor(model string) (ports.LLMClient, error) {
	return r.llm, nil
}

func (r *fakeRouter) AllModels() []string {
	return r.models
}

func (r *fakeRouter) Probe(ctx context.Context) error { return nil }

// TestDebateRefinesLeafResult verifica que con el debate activado, el
// resultado de la hoja pasa por crítica+refinamiento antes de sintetizar, y
// que el razonamiento del debate se emite.
func TestDebateRefinesLeafResult(t *testing.T) {
	fake := &fakeLLM{}
	// 1) descomposición atómica; 2) hoja; 3) crítica [APROBADA]; 4) síntesis.
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"resultado hoja"}, nil)
	fake.queueCompletion("[APROBADA]")
	fake.queueStream([]string{"respuesta final"}, nil)

	router := &fakeRouter{llm: fake, models: []string{"test-model"}}
	opts := Options{
		Model:                 "test-model",
		MaxDecompositionDepth: 3,
		MaxToolRoundsPerPhase: 25,
		Debate: &DebateOptions{
			Enabled: true,
			Rounds:  2,
			Router:  router,
		},
	}
	engine := New(router, opts)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "haz algo simple"})

	var events []Event
	if err := engine.Run(context.Background(), collectEvents(&events)); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if got := contentOf(events); got != "respuesta final" {
		t.Errorf("expected content %q, got %q", "respuesta final", got)
	}
	var reasoning strings.Builder
	for _, ev := range events {
		if ev.Kind == EventReasoning {
			reasoning.WriteString(ev.Text)
		}
	}
	if !strings.Contains(reasoning.String(), "[Speculum]") {
		t.Errorf("expected speculum reasoning to be emitted, got %q", reasoning.String())
	}
	if !strings.Contains(reasoning.String(), "crítico") {
		t.Errorf("expected critic reasoning, got %q", reasoning.String())
	}
}

// TestDebateDisabledLeavesResultUnchanged verifica que sin debate el resultado
// de la hoja no se refina (no hay llamadas de crítica).
func TestDebateDisabledLeavesResultUnchanged(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"resultado hoja"}, nil)
	fake.queueStream([]string{"respuesta final"}, nil)

	router := &fakeRouter{llm: fake, models: []string{"test-model"}}
	opts := Options{
		Model:                 "test-model",
		MaxDecompositionDepth: 3,
		MaxToolRoundsPerPhase: 25,
	}
	engine := New(router, opts)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "haz algo simple"})

	if err := engine.Run(context.Background(), func(Event) error { return nil }); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// 1 completion (descomposición) + 2 streams (hoja + síntesis). Sin crítica.
	if got := fake.count(); got != 3 {
		t.Errorf("expected 3 calls (no debate), got %d", got)
	}
}
