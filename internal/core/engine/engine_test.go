package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ai-proxy-agent-harness/internal/core/goal"
	"ai-proxy-agent-harness/internal/core/openai"
)

func newTestEngine(t *testing.T, f *fakeLLM, tools []openai.Tool, toolChoice json.RawMessage, maxToolRounds int) *Engine {
	t.Helper()
	opts := Options{
		Model:                 "test-model",
		Tools:                 tools,
		ToolChoice:            toolChoice,
		MaxDecompositionDepth: 3,
		MaxToolRoundsPerPhase: maxToolRounds,
	}
	if maxToolRounds == 0 {
		opts.MaxToolRoundsPerPhase = 25
	}
	return New(f, opts)
}

func collectEvents(events *[]Event) Handler {
	return func(ev Event) error {
		*events = append(*events, ev)
		return nil
	}
}

func runUntilToolCall(t *testing.T, engine *Engine) {
	t.Helper()
	err := engine.Run(context.Background(), func(ev Event) error {
		if ev.Kind == EventToolCalls {
			return errStop
		}
		return nil
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("expected run to stop at tool call, got err: %v", err)
	}
}

func contentOf(events []Event) string {
	var parts []string
	for _, ev := range events {
		if ev.Kind == EventContent {
			parts = append(parts, ev.Text)
		}
	}
	return strings.Join(parts, "")
}

func systemOf(req recordedRequest) string {
	if len(req.messages) == 0 || req.messages[0].Content == nil {
		return ""
	}
	return req.messages[0].Content.Text
}

func userTextOf(req recordedRequest) string {
	if len(req.messages) < 2 || req.messages[1].Content == nil {
		return ""
	}
	return req.messages[1].Content.Text
}

func TestFullRunBasicFlow(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"resultado de la tarea"}, nil)
	fake.queueStream([]string{"respuesta final"}, nil)

	engine := newTestEngine(t, fake, nil, nil, 0)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "haz algo simple"})

	var events []Event
	if err := engine.Run(context.Background(), collectEvents(&events)); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if got := contentOf(events); got != "respuesta final" {
		t.Errorf("expected content %q, got %q", "respuesta final", got)
	}
	for _, ev := range events {
		if ev.Kind == EventToolCalls {
			t.Errorf("unexpected tool_calls event")
		}
	}
}

func TestMultiRoundToolCallsPreserveHistoryWithinLeaf(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream(nil, []openai.ToolCall{{ID: "call_1", Type: "function", Function: openai.FunctionCall{Name: "leer", Arguments: "{}"}}})
	fake.queueStream(nil, []openai.ToolCall{{ID: "call_2", Type: "function", Function: openai.FunctionCall{Name: "escribir", Arguments: "{}"}}})
	fake.queueStream([]string{"listo"}, nil)
	fake.queueStream([]string{"respuesta final"}, nil)

	engine := newTestEngine(t, fake, []openai.Tool{{Type: "function", Function: openai.FunctionDef{Name: "leer"}}}, nil, 0)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "usa herramientas"})

	runUntilToolCall(t, engine)
	if engine.PendingPhase() != PhaseLeaf {
		t.Fatalf("expected pending phase leaf, got %v", engine.PendingPhase())
	}
	if calls := engine.PendingToolCalls(); len(calls) != 1 || calls[0].ID != "call_1" {
		t.Fatalf("expected pending call_1, got %#v", calls)
	}

	// Segunda ronda dentro de la misma hoja.
	var events2 []Event
	err := engine.Resume(context.Background(), map[string]string{"call_1": "contenido leído"}, func(ev Event) error {
		if ev.Kind == EventToolCalls {
			return errStop
		}
		events2 = append(events2, ev)
		return nil
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("expected resume to stop at second tool call, got: %v", err)
	}
	if engine.PendingPhase() != PhaseLeaf {
		t.Fatalf("expected still pending leaf")
	}
	if calls := engine.PendingToolCalls(); len(calls) != 1 || calls[0].ID != "call_2" {
		t.Fatalf("expected pending call_2, got %#v", calls)
	}

	round2 := fake.recordAt(2)
	if !hasToolResult(round2.messages, "call_1") {
		t.Errorf("round 2 should carry the tool result for call_1")
	}

	// Tercera ronda: completa la hoja y la síntesis.
	var events3 []Event
	if err := engine.Resume(context.Background(), map[string]string{"call_2": "escrito ok"}, collectEvents(&events3)); err != nil {
		t.Fatalf("Resume() error: %v", err)
	}

	round3 := fake.recordAt(3)
	if !hasToolResult(round3.messages, "call_1") || !hasToolResult(round3.messages, "call_2") {
		t.Errorf("round 3 should carry both tool results")
	}
	if got := contentOf(events3); got != "respuesta final" {
		t.Errorf("expected content %q, got %q", "respuesta final", got)
	}
}

func hasToolResult(messages []openai.Message, toolCallID string) bool {
	for _, message := range messages {
		if message.Role == openai.RoleTool && message.ToolCallID != nil && *message.ToolCallID == toolCallID {
			return true
		}
	}
	return false
}

func TestToolCallDuringSynthesisIsResumable(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"resultado atómico"}, nil)
	fake.queueStream(nil, []openai.ToolCall{{ID: "call_s1", Type: "function", Function: openai.FunctionCall{Name: "verificar", Arguments: "{}"}}})
	fake.queueStream([]string{"respuesta final verificada"}, nil)

	engine := newTestEngine(t, fake, []openai.Tool{{Type: "function", Function: openai.FunctionDef{Name: "verificar"}}}, nil, 0)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "haz algo y verifica"})

	runUntilToolCall(t, engine)
	if engine.PendingPhase() != PhaseSynthesis {
		t.Fatalf("expected pending phase synthesis, got %v", engine.PendingPhase())
	}

	var events []Event
	if err := engine.Resume(context.Background(), map[string]string{"call_s1": "verificado ok"}, collectEvents(&events)); err != nil {
		t.Fatalf("Resume() error: %v", err)
	}
	if got := contentOf(events); got != "respuesta final verificada" {
		t.Errorf("expected content %q, got %q", "respuesta final verificada", got)
	}
	if engine.PendingPhase() != PhaseNone {
		t.Errorf("expected pending cleared, got %v", engine.PendingPhase())
	}
}

func TestForcedToolChoiceFromCallerNotPropagatedInternally(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"ok"}, nil)
	fake.queueStream([]string{"final"}, nil)

	engine := newTestEngine(t, fake,
		[]openai.Tool{{Type: "function", Function: openai.FunctionDef{Name: "f"}}},
		json.RawMessage(`{"type":"function","function":{"name":"f"}}`), 0)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "haz algo"})

	var events []Event
	if err := engine.Run(context.Background(), collectEvents(&events)); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	for _, index := range []int{1, 2} {
		req := fake.recordAt(index)
		if got := string(req.toolChoice); got != `"auto"` {
			t.Errorf("record %d: expected internal tool_choice auto, got %s", index, got)
		}
	}
}

func TestToolRoundLimitForcesFinalText(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream(nil, []openai.ToolCall{{ID: "c1", Type: "function", Function: openai.FunctionCall{Name: "f", Arguments: "{}"}}})
	fake.queueStream(nil, []openai.ToolCall{{ID: "c2", Type: "function", Function: openai.FunctionCall{Name: "f", Arguments: "{}"}}})
	fake.queueStream([]string{"forzado a texto"}, nil)
	fake.queueStream([]string{"síntesis"}, nil)

	engine := newTestEngine(t, fake,
		[]openai.Tool{{Type: "function", Function: openai.FunctionDef{Name: "f"}}}, nil, 2)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "intenta usar tools sin parar"})

	runUntilToolCall(t, engine)
	if engine.ToolRoundCount() != 1 {
		t.Fatalf("expected round count 1, got %d", engine.ToolRoundCount())
	}

	var events2 []Event
	err := engine.Resume(context.Background(), map[string]string{"c1": "r1"}, func(ev Event) error {
		if ev.Kind == EventToolCalls {
			return errStop
		}
		events2 = append(events2, ev)
		return nil
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("expected resume to stop at round 2, got: %v", err)
	}
	if engine.ToolRoundCount() != 2 {
		t.Fatalf("expected round count 2, got %d", engine.ToolRoundCount())
	}

	var events3 []Event
	if err := engine.Resume(context.Background(), map[string]string{"c2": "r2"}, collectEvents(&events3)); err != nil {
		t.Fatalf("final Resume() error: %v", err)
	}

	thirdLeafCall := fake.recordAt(3)
	if len(thirdLeafCall.tools) != 0 {
		t.Errorf("third leaf call should not offer tools")
	}
	for _, ev := range events3 {
		if ev.Kind == EventToolCalls {
			t.Errorf("unexpected tool_calls event after hitting the limit")
		}
	}
}

func TestCallerSystemPromptIsComposedNotReplaced(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"ok"}, nil)
	fake.queueStream([]string{"final"}, nil)

	engine := newTestEngine(t, fake, nil, nil, 0)
	engine.SetGoalContext(&goal.Context{
		CallerSystem:    "Eres Codex, un agente de código con reglas X.",
		TurnInstruction: "arregla el bug",
	})

	var events []Event
	if err := engine.Run(context.Background(), collectEvents(&events)); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	decompositionSystem := systemOf(fake.recordAt(0))
	if !strings.Contains(decompositionSystem, "Eres Codex, un agente de código con reglas X.") {
		t.Errorf("decomposition system missing caller system")
	}
	if !strings.Contains(decompositionSystem, "planificador de tareas") {
		t.Errorf("decomposition system missing phase role")
	}

	leafSystem := systemOf(fake.recordAt(1))
	if !strings.Contains(leafSystem, "Eres Codex, un agente de código con reglas X.") {
		t.Errorf("leaf system missing caller system")
	}
}

func TestDecompositionSeesToolDescriptionsAsText(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"ok"}, nil)
	fake.queueStream([]string{"final"}, nil)

	engine := newTestEngine(t, fake,
		[]openai.Tool{{Type: "function", Function: openai.FunctionDef{Name: "get_weather", Description: "Devuelve el clima de una ciudad"}}}, nil, 0)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "clima en Paris"})

	var events []Event
	if err := engine.Run(context.Background(), collectEvents(&events)); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	decomposition := fake.recordAt(0)
	userText := userTextOf(decomposition)
	if !strings.Contains(userText, "get_weather") {
		t.Errorf("decomposition user should mention the tool name")
	}
	if !strings.Contains(userText, "Devuelve el clima de una ciudad") {
		t.Errorf("decomposition user should mention the tool description")
	}
	if decomposition.jsonMode == false {
		t.Errorf("decomposition should use json_mode")
	}
	if len(decomposition.tools) != 0 {
		t.Errorf("decomposition phase should not receive functional tools")
	}
}

func TestDecompositionWithoutToolsShowsPlaceholder(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"ok"}, nil)
	fake.queueStream([]string{"final"}, nil)

	engine := newTestEngine(t, fake, nil, nil, 0)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "haz algo"})

	var events []Event
	if err := engine.Run(context.Background(), collectEvents(&events)); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if userText := userTextOf(fake.recordAt(0)); !strings.Contains(userText, "ninguna herramienta disponible") {
		t.Errorf("decomposition user should show the no-tools placeholder")
	}
}

func TestImagesAreAttachedToEveryPhase(t *testing.T) {
	imagePart := openai.NewImagePart("data:image/png;base64,xxx")
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"ok"}, nil)
	fake.queueStream([]string{"final"}, nil)

	engine := newTestEngine(t, fake, nil, nil, 0)
	engine.SetGoalContext(&goal.Context{
		TurnInstruction: "describe la imagen",
		ImageParts:      []openai.ContentPart{imagePart},
	})

	var events []Event
	if err := engine.Run(context.Background(), collectEvents(&events)); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	for _, index := range []int{0, 1, 2} {
		req := fake.recordAt(index)
		userContent := req.messages[1].Content
		if userContent == nil || len(userContent.Parts) != 1 {
			t.Errorf("record %d: expected image part on user content", index)
			continue
		}
		if userContent.Parts[0].Type != openai.PartTypeImage {
			t.Errorf("record %d: expected image part, got %s", index, userContent.Parts[0].Type)
		}
	}
}

func TestDecompositionRecursionProducesTree(t *testing.T) {
	fake := &fakeLLM{}
	fake.queueCompletion(`{"atomic": false, "subtasks": ["paso A", "paso B"]}`)
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueCompletion(`{"atomic": true, "subtasks": []}`)
	fake.queueStream([]string{"ok A"}, nil)
	fake.queueStream([]string{"ok B"}, nil)
	fake.queueStream([]string{"final"}, nil)

	engine := newTestEngine(t, fake, nil, nil, 0)
	engine.SetGoalContext(&goal.Context{TurnInstruction: "tarea compuesta"})

	var events []Event
	if err := engine.Run(context.Background(), collectEvents(&events)); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// 1 raíz + 2 hijos (descomposición) + 2 hojas + 1 síntesis.
	if fake.count() != 6 {
		t.Fatalf("expected 6 upstream calls, got %d", fake.count())
	}
	root := engine.Root()
	if root == nil || len(root.Children) != 2 {
		t.Fatalf("expected root with 2 children")
	}
	for _, child := range root.Children {
		if !child.IsAtomic {
			t.Errorf("expected child %q to be atomic", child.Description)
		}
	}
	if got := contentOf(events); got != "final" {
		t.Errorf("expected final content, got %q", got)
	}
}

func TestParseDecompositionFallback(t *testing.T) {
	if parsed := parseDecomposition(`{"atomic": true, "subtasks": []}`); !parsed.Atomic {
		t.Errorf("expected atomic")
	}
	parsed := parseDecomposition("texto antes\n{\"atomic\": false, \"subtasks\": [\"a\", \"b\"]}\ntexto después")
	if parsed.Atomic {
		t.Errorf("expected non-atomic from embedded object")
	}
	if len(parsed.Subtasks) != 2 {
		t.Errorf("expected 2 subtasks, got %#v", parsed.Subtasks)
	}
	if parsed := parseDecomposition("no json aqui"); !parsed.Atomic {
		t.Errorf("expected atomic fallback on garbage")
	}
	// Llaves dentro de strings no deben romper el balanceo.
	parsed = parseDecomposition(`{"atomic": false, "subtasks": ["a {b} c"]}`)
	if parsed.Atomic || len(parsed.Subtasks) != 1 || parsed.Subtasks[0] != "a {b} c" {
		t.Errorf("braces inside strings must not break parsing: %#v", parsed)
	}
}
