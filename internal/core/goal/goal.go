// Package goal separa una request entrante en las partes con distinto rol:
// el system prompt del caller (autoridad real, no dato a clasificar), la
// instrucción del turno actual (lo único que se descompone), el contexto de
// turnos previos (fondo, nunca redecompuesto) y las partes de imagen de toda
// la conversación.
package goal

import (
	"strings"

	"ai-proxy-agent-harness/internal/core/openai"
)

// Context agrupa las partes de una request con roles diferenciados.
type Context struct {
	CallerSystem    string
	TurnInstruction string
	PriorContext    string
	ImageParts      []openai.ContentPart
}

// Extract separa los mensajes de una request en su GoalContext. Si
// priorContextOverride no es nil, reemplaza el contexto previo calculado
// (usado al sembrar un turno nuevo con el resumen de turnos ya resueltos).
func Extract(messages []openai.Message, priorContextOverride *string) Context {
	var callerSystemParts []string
	for _, message := range messages {
		if message.Role != openai.RoleSystem {
			continue
		}
		if text := messageText(message); text != "" {
			callerSystemParts = append(callerSystemParts, text)
		}
	}

	boundary := findTurnBoundary(messages)

	var before, after []openai.Message
	for index, message := range messages {
		if message.Role == openai.RoleSystem {
			continue
		}
		if index <= boundary {
			before = append(before, message)
		} else {
			after = append(after, message)
		}
	}

	turnInstruction := flattenMessages(after)
	priorContext := flattenMessages(before)
	if priorContextOverride != nil {
		priorContext = *priorContextOverride
	}

	if strings.TrimSpace(turnInstruction) == "" {
		// Caso borde defensivo: si no hay nada claramente "posterior al último
		// turno del assistant", no dejamos que el motor descomponga una
		// instrucción vacía: usamos todo el historial no-system como turno.
		var allNonSystem []openai.Message
		for _, message := range messages {
			if message.Role != openai.RoleSystem {
				allNonSystem = append(allNonSystem, message)
			}
		}
		turnInstruction = flattenMessages(allNonSystem)
		priorContext = ""
		if priorContextOverride != nil {
			priorContext = *priorContextOverride
		}
	}

	var imageParts []openai.ContentPart
	for _, message := range messages {
		if message.Content != nil {
			imageParts = append(imageParts, message.Content.Parts...)
		}
	}

	return Context{
		CallerSystem:    strings.Join(callerSystemParts, "\n\n"),
		TurnInstruction: turnInstruction,
		PriorContext:    priorContext,
		ImageParts:      imageParts,
	}
}

// findTurnBoundary devuelve el índice del último mensaje assistant con texto
// real (una respuesta final ya entregada). Todo lo posterior es "el turno
// actual"; todo lo anterior es contexto de fondo.
//
// Un assistant que solo hizo tool_calls sin texto no cuenta como cierre de
// turno: es un intercambio todavía abierto y debe quedar dentro de la
// instrucción del turno.
func findTurnBoundary(messages []openai.Message) int {
	boundary := -1
	for index, message := range messages {
		if message.Role == openai.RoleAssistant && messageText(message) != "" {
			boundary = index
		}
	}
	return boundary
}

// flattenMessages aplana una lista de mensajes a texto plano, tolerante a
// contenido multimodal (usa solo la parte de texto). Se usa para renderizar
// tanto el contexto previo como la instrucción del turno actual.
func flattenMessages(messages []openai.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		text := messageText(message)
		switch {
		case message.Role == openai.RoleTool && text != "":
			label := "resultado de herramienta"
			if message.Name != nil && *message.Name != "" {
				label = "resultado de herramienta " + *message.Name
			}
			parts = append(parts, "["+label+"] "+text)
		case text != "":
			parts = append(parts, "["+string(message.Role)+"] "+text)
		case len(message.ToolCalls) > 0:
			calls := make([]string, 0, len(message.ToolCalls))
			for _, toolCall := range message.ToolCalls {
				calls = append(calls, toolCall.Function.Name+"("+toolCall.Function.Arguments+")")
			}
			parts = append(parts, "["+string(message.Role)+"] (llamó a herramienta(s): "+strings.Join(calls, "; ")+")")
		}
	}
	return strings.Join(parts, "\n\n")
}

func messageText(message openai.Message) string {
	if message.Content == nil {
		return ""
	}
	return message.Content.Text
}
