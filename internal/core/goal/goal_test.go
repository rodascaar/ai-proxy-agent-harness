package goal

import (
	"testing"

	"ai-proxy-agent-harness/internal/core/openai"
)

func textContent(s string) *openai.Content {
	return &openai.Content{Text: s}
}

func user(s string) openai.Message {
	return openai.Message{Role: openai.RoleUser, Content: textContent(s)}
}

func assistant(s string) openai.Message {
	return openai.Message{Role: openai.RoleAssistant, Content: textContent(s)}
}

func TestExtractCallerSystemAndTurn(t *testing.T) {
	messages := []openai.Message{
		{Role: openai.RoleSystem, Content: textContent("eres un asistente")},
		user("haz A"),
		assistant("hecho A"),
		user("ahora haz B"),
	}
	ctx := Extract(messages, nil)
	if ctx.CallerSystem != "eres un asistente" {
		t.Errorf("unexpected caller system: %q", ctx.CallerSystem)
	}
	if ctx.TurnInstruction != "[user] ahora haz B" {
		t.Errorf("unexpected turn instruction: %q", ctx.TurnInstruction)
	}
	if ctx.PriorContext != "[user] haz A\n\n[assistant] hecho A" {
		t.Errorf("unexpected prior context: %q", ctx.PriorContext)
	}
}

func TestExtractMultipleSystemMessages(t *testing.T) {
	messages := []openai.Message{
		{Role: openai.RoleSystem, Content: textContent("regla 1")},
		{Role: openai.RoleSystem, Content: textContent("regla 2")},
		user("hola"),
	}
	ctx := Extract(messages, nil)
	if ctx.CallerSystem != "regla 1\n\nregla 2" {
		t.Errorf("unexpected caller system: %q", ctx.CallerSystem)
	}
}

func TestExtractNoAssistantYet(t *testing.T) {
	messages := []openai.Message{
		{Role: openai.RoleSystem, Content: textContent("eres un asistente")},
		user("haz A"),
	}
	ctx := Extract(messages, nil)
	if ctx.TurnInstruction != "[user] haz A" {
		t.Errorf("unexpected turn instruction: %q", ctx.TurnInstruction)
	}
	if ctx.PriorContext != "" {
		t.Errorf("expected empty prior context, got %q", ctx.PriorContext)
	}
}

func TestExtractAssistantToolCallsDoNotCloseTurn(t *testing.T) {
	// Un assistant que solo hizo tool_calls no cierra el turno: el intercambio
	// queda abierto y debe ir junto a su resultado de tool en turn_instruction.
	toolCall := openai.ToolCall{ID: "call_1", Function: openai.FunctionCall{Name: "leer", Arguments: "{}"}}
	messages := []openai.Message{
		user("lee el archivo"),
		{Role: openai.RoleAssistant, ToolCalls: []openai.ToolCall{toolCall}},
		{Role: openai.RoleTool, Content: textContent("contenido"), ToolCallID: &toolCall.ID},
	}
	ctx := Extract(messages, nil)
	if ctx.PriorContext != "" {
		t.Errorf("expected empty prior context, got %q", ctx.PriorContext)
	}
	wantTurn := "[user] lee el archivo\n\n[assistant] (llamó a herramienta(s): leer({}))\n\n[resultado de herramienta] contenido"
	if ctx.TurnInstruction != wantTurn {
		t.Errorf("unexpected turn instruction:\nwant %q\ngot  %q", wantTurn, ctx.TurnInstruction)
	}
}

func TestExtractToolMessageWithName(t *testing.T) {
	name := "buscador"
	messages := []openai.Message{
		user("busca"),
		{Role: openai.RoleTool, Content: textContent("resultado"), Name: &name},
	}
	ctx := Extract(messages, nil)
	if ctx.TurnInstruction != "[user] busca\n\n[resultado de herramienta buscador] resultado" {
		t.Errorf("unexpected turn instruction: %q", ctx.TurnInstruction)
	}
}

func TestExtractPriorContextOverride(t *testing.T) {
	messages := []openai.Message{
		user("turno viejo"),
		assistant("respuesta vieja"),
		user("turno nuevo"),
	}
	override := "resumen de turnos previos"
	ctx := Extract(messages, &override)
	if ctx.PriorContext != override {
		t.Errorf("expected override, got %q", ctx.PriorContext)
	}
	if ctx.TurnInstruction != "[user] turno nuevo" {
		t.Errorf("unexpected turn instruction: %q", ctx.TurnInstruction)
	}
}

func TestExtractImagePartsCollectedAcrossMessages(t *testing.T) {
	image := openai.NewImagePart("data:image/png;base64,AAA")
	messages := []openai.Message{
		{Role: openai.RoleUser, Content: &openai.Content{Text: "describe", Parts: []openai.ContentPart{image}}},
	}
	ctx := Extract(messages, nil)
	if len(ctx.ImageParts) != 1 || ctx.ImageParts[0].Type != openai.PartTypeImage {
		t.Fatalf("expected 1 image part, got %#v", ctx.ImageParts)
	}
}

func TestExtractFallbackWhenTurnInstructionEmpty(t *testing.T) {
	// Caso borde: sin ningún turno claramente "posterior", se usa todo el
	// historial no-system como instrucción para no descomponer vacío.
	messages := []openai.Message{
		{Role: openai.RoleSystem, Content: textContent("eres un asistente")},
		{Role: openai.RoleAssistant, ToolCalls: []openai.ToolCall{{ID: "c1"}}},
	}
	ctx := Extract(messages, nil)
	if ctx.TurnInstruction == "" {
		t.Errorf("expected non-empty turn instruction in fallback")
	}
}
