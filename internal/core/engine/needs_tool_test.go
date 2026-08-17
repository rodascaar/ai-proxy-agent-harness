package engine

import (
	"context"
	"strings"
	"testing"

	"ai-proxy-agent-harness/internal/core/goal"
	"ai-proxy-agent-harness/internal/core/openai"
)

// TestNeedsToolMarkerTriggersCorrectiveRetry verifica el fix del caso "hola":
// sin tools del caller, si la hoja emite el marcador [[NECESITA_HERRAMIENTA]],
// el motor reintenta UNA vez pidiendo respuesta directa y el reintento gana.
func TestNeedsToolMarkerTriggersCorrectiveRetry(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"Explicación: [[NECESITA_HERRAMIENTA: leer el archivo config.yaml]]\n```python\nprint('falso')\n```"}, nil)
	fake.queueStream([]string{"¡Hola! ¿En qué puedo ayudarte?"}, nil)
	fake.queueStream([]string{"¡Hola!"}, nil)

	engine := newTestEngine(t, fake, nil, nil, 0)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "hola"})

	var events []Event
	if err := engine.Run(context.Background(), collectEvents(&events)); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// 1 descomposición + hoja con marcador + reintento correctivo + síntesis.
	if fake.count() != 4 {
		t.Fatalf("expected 4 upstream calls (with corrective retry), got %d", fake.count())
	}

	// El reintento (record 2) debe llevar el mensaje de corrección.
	retry := fake.recordAt(2)
	foundCorrection := false
	for _, msg := range retry.messages {
		if msg.Role == openai.RoleUser && msg.Content != nil && strings.Contains(msg.Content.Text, "CORRECCIÓN") {
			foundCorrection = true
			break
		}
	}
	if !foundCorrection {
		t.Errorf("corrective retry should include the CORRECCIÓN user message")
	}

	// El contexto de la síntesis NO debe contener el marcador.
	synthesis := userTextOf(fake.recordAt(3))
	if strings.Contains(synthesis, "NECESITA_HERRAMIENTA") {
		t.Errorf("synthesis context should not carry the marker")
	}
	if !strings.Contains(synthesis, "¡Hola! ¿En qué puedo ayudarte?") {
		t.Errorf("synthesis context should carry the corrected leaf result")
	}

	// La respuesta final visible es la síntesis.
	if got := contentOf(events); got != "¡Hola!" {
		t.Errorf("expected final content %q, got %q", "¡Hola!", got)
	}
}

// TestNeedsToolMarkerPersistingUsesFallback verifica que si el reintento
// correctivo vuelve a emitir el marcador, la hoja queda con una nota honesta
// de pendiente en lugar de contenido inventado.
func TestNeedsToolMarkerPersistingUsesFallback(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"[[NECESITA_HERRAMIENTA: ejecutar un comando]]"}, nil)
	fake.queueStream([]string{"[[NECESITA_HERRAMIENTA: ejecutar un comando]]"}, nil)
	fake.queueStream([]string{"respuesta final"}, nil)

	engine := newTestEngine(t, fake, nil, nil, 0)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "haz algo"})

	if err := engine.Run(context.Background(), func(Event) error { return nil }); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	synthesis := userTextOf(fake.recordAt(3))
	if !strings.Contains(synthesis, "[Pendiente: esta subtarea requiere ejecutar un comando") {
		t.Errorf("synthesis context should carry the honest fallback note, got: %s", synthesis)
	}
	if strings.Contains(synthesis, "NECESITA_HERRAMIENTA") {
		t.Errorf("synthesis context should not carry the raw marker")
	}
}

// TestNeedsToolMarkerWithToolsIsPreserved verifica que con tools del caller el
// marcador NO dispara el reintento (es el protocolo legítimo de pendientes que
// la síntesis puede resolver con una herramienta).
func TestNeedsToolMarkerWithToolsIsPreserved(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"[[NECESITA_HERRAMIENTA: leer archivo remoto]]"}, nil)
	fake.queueStream([]string{"respuesta final"}, nil)

	engine := newTestEngine(t, fake,
		[]openai.Tool{{Type: "function", Function: openai.FunctionDef{Name: "leer"}}}, nil, 0)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "lee el archivo remoto"})

	if err := engine.Run(context.Background(), func(Event) error { return nil }); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Sin reintento: descomposición + hoja + síntesis.
	if fake.count() != 3 {
		t.Fatalf("expected 3 upstream calls (no retry with tools), got %d", fake.count())
	}
	synthesis := userTextOf(fake.recordAt(2))
	if !strings.Contains(synthesis, "[[NECESITA_HERRAMIENTA") {
		t.Errorf("with tools the marker should reach synthesis for the pending protocol")
	}
}

// TestNeedsToolHelpers cubre la detección del marcador y su sanitización.
func TestNeedsToolHelpers(t *testing.T) {
	if got := extractNeedsToolDescription("[[NECESITA_HERRAMIENTA: leer un archivo]]"); got != "leer un archivo" {
		t.Errorf("unexpected description: %q", got)
	}
	if got := extractNeedsToolDescription("texto antes [[NECESITA_HERRAMIENTA: correr un test]] texto después"); got != "correr un test" {
		t.Errorf("unexpected embedded description: %q", got)
	}
	if got := extractNeedsToolDescription("sin marcador"); got != "" {
		t.Errorf("expected empty description, got %q", got)
	}
	// Marcador sin descripción.
	if got := extractNeedsToolDescription("[[NECESITA_HERRAMIENTA]]"); got != "" {
		t.Errorf("expected empty description for bare marker, got %q", got)
	}

	sanitized := sanitizeNeedsToolMarker("resultado con [[NECESITA_HERRAMIENTA: leer el archivo]] dentro")
	if strings.Contains(sanitized, "NECESITA_HERRAMIENTA") {
		t.Errorf("sanitized text should not contain the marker: %q", sanitized)
	}
	if !strings.Contains(sanitized, "[Pendiente: esta subtarea requiere leer el archivo") {
		t.Errorf("sanitized text should carry the pending note: %q", sanitized)
	}
	if got := needsToolFallback(""); !strings.Contains(got, "no disponible") {
		t.Errorf("fallback without description should mention unavailability: %q", got)
	}
}
