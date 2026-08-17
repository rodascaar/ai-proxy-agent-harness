package prompts

import (
	"strings"
	"testing"
)

func TestRenderDecompositionUser(t *testing.T) {
	rendered, err := Render(DecompositionUser, map[string]string{
		PlaceholderGoal:         "escribe una función y una prueba",
		PlaceholderPriorContext: "(sin contexto previo)",
		PlaceholderTools:        "- leer: lee un archivo",
		PlaceholderTask:         "la tarea a evaluar",
	})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	for _, want := range []string{
		"escribe una función y una prueba",
		"(sin contexto previo)",
		"- leer: lee un archivo",
		"la tarea a evaluar",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered prompt missing %q", want)
		}
	}
}

func TestRenderKeepsUnknownPlaceholders(t *testing.T) {
	// Si un template introduce un placeholder no soportado, no debe romper el
	// render: es un bug del template, no del llamador.
	rendered, err := Render(DecompositionSystem, map[string]string{})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	if !strings.Contains(rendered, "planificador de tareas") {
		t.Errorf("decomposition system prompt content missing")
	}
}

func TestRenderCallerSystemPreamble(t *testing.T) {
	rendered, err := Render(CallerSystemPreamble, map[string]string{
		PlaceholderCallerSystem: "Eres Codex, un agente de código.",
	})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	if !strings.Contains(rendered, "Eres Codex, un agente de código.") {
		t.Errorf("caller system preamble missing caller system")
	}
}

func TestRenderExecuteAndSynthesisWithTools(t *testing.T) {
	for _, tc := range []struct {
		template string
		values   map[string]string
	}{
		{
			template: ExecuteAtomicUser,
			values: map[string]string{
				PlaceholderGoal:         "saluda",
				PlaceholderPriorContext: "(sin contexto previo)",
				PlaceholderContext:      "(ninguno todavía)",
				PlaceholderTools:        "ninguna herramienta disponible",
				PlaceholderTask:         "la tarea atómica",
			},
		},
		{
			template: SynthesisUser,
			values: map[string]string{
				PlaceholderGoal:         "saluda",
				PlaceholderPriorContext: "(sin contexto previo)",
				PlaceholderContext:      "(sin resultados)",
				PlaceholderTools:        "- leer: lee un archivo",
			},
		},
	} {
		rendered, err := Render(tc.template, tc.values)
		if err != nil {
			t.Fatalf("Render(%s) error: %v", tc.template, err)
		}
		if !strings.Contains(rendered, "<tools_disponibles>") {
			t.Errorf("template %s should expose <tools_disponibles>", tc.template)
		}
		if !strings.Contains(rendered, tc.values[PlaceholderTools]) {
			t.Errorf("template %s missing tools value", tc.template)
		}
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	if _, err := Render("does_not_exist.md", nil); err == nil {
		t.Fatalf("expected error for unknown template")
	}
}
