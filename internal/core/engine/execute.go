package engine

import (
	"context"
	"fmt"
	"strings"

	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
	"ai-proxy-agent-harness/internal/core/task"
	"ai-proxy-agent-harness/internal/prompts"
)

// phaseResult describe el desenlace de una ronda de runPhase.
type phaseResult struct {
	paused    bool
	text      string
	toolCalls []openai.ToolCall
}

// runPhase ejecuta una sola ronda de streaming para la fase en curso (hoja
// atómica o síntesis). No sabe cuál de las dos es: solo construye mensajes,
// aplica la política de tools/tool_choice y el límite de rondas, y emite
// eventos de reasoning/content o de tool_calls. La orquestación (guardar
// resultado de hoja, marcar pendiente, seguir con la siguiente hoja) la hace
// el llamador.
func (e *Engine) runPhase(ctx context.Context, system, userText string, emitKind EventKind, extra []openai.Message, onEvent Handler) (phaseResult, error) {
	messages := []openai.Message{
		{Role: openai.RoleSystem, Content: messageContent(system)},
		{Role: openai.RoleUser, Content: e.buildUserContent(userText)},
	}
	messages = append(messages, extra...)

	phaseTools, phaseToolChoice := e.normalizeToolChoice()
	if phaseTools != nil && e.toolRoundCount >= e.opts.MaxToolRoundsPerPhase {
		phaseTools, phaseToolChoice = nil, nil
		if err := emitReasoning(onEvent, "\n\n[límite de rondas de herramientas alcanzado, respondo con lo que hay disponible]\n\n"); err != nil {
			return phaseResult{}, err
		}
	}

	resultParts := make([]string, 0, 8)
	toolCallAcc := map[int]*openai.ToolCall{}

	err := e.llm.Stream(ctx, ports.StreamRequest{
		Model:      e.opts.Model,
		Messages:   messages,
		Tools:      phaseTools,
		ToolChoice: phaseToolChoice,
	}, func(chunk ports.StreamChunk) error {
		if piece := chunk.Delta.Content; piece != nil && *piece != "" {
			resultParts = append(resultParts, *piece)
			if err := onEvent(Event{Kind: emitKind, Text: *piece}); err != nil {
				return err
			}
		}
		if len(chunk.Delta.ToolCalls) > 0 {
			mergeToolCallDelta(toolCallAcc, chunk.Delta.ToolCalls)
		}
		return nil
	})
	if err != nil {
		return phaseResult{}, err
	}

	resultText := strings.Join(resultParts, "")
	if len(toolCallAcc) > 0 {
		toolCalls := collectToolCalls(toolCallAcc)
		if emitKind == EventReasoning {
			if err := emitReasoning(onEvent, "\n\nSe requiere usar una herramienta antes de continuar; interrumpo el resto del proceso.\n\n"); err != nil {
				return phaseResult{}, err
			}
		}
		// No emitimos el evento EventToolCalls aquí: el llamador debe fijar
		// primero el estado pendiente (enterPending) y emitirlo después, para
		// que una interrupción del handler no deje el motor sin estado.
		return phaseResult{paused: true, text: resultText, toolCalls: toolCalls}, nil
	}

	if emitKind == EventReasoning {
		if err := emitReasoning(onEvent, "\n\n"); err != nil {
			return phaseResult{}, err
		}
	}
	return phaseResult{paused: false, text: resultText}, nil
}

// collectToolCalls ordena los tool calls acumulados por índice.
func collectToolCalls(acc map[int]*openai.ToolCall) []openai.ToolCall {
	toolCalls := make([]openai.ToolCall, 0, len(acc))
	for index := 0; index < len(acc); index++ {
		if entry, ok := acc[index]; ok {
			toolCalls = append(toolCalls, *entry)
		}
	}
	return toolCalls
}

// enterPending guarda el estado de pausa tras una ronda con tool_calls,
// añadiendo el mensaje assistant a la conversación pendiente (append-only).
func (e *Engine) enterPending(phase Phase, leafIndex int, resultText string, toolCalls []openai.ToolCall) {
	e.toolRoundCount++
	e.pendingConversation = append(e.pendingConversation, openai.Message{
		Role:      openai.RoleAssistant,
		Content:   messageContent(resultText),
		ToolCalls: toolCalls,
	})
	e.pendingPhase = phase
	e.pendingLeafIndex = leafIndex
	e.pendingToolCalls = toolCalls
}

// pausePhase fija el estado pendiente de una pausa y emite el evento
// EventToolCalls. Orden intencional: el estado se persiste ANTES de emitir el
// evento, para que una interrupción del handler no deje al motor sin estado.
func (e *Engine) pausePhase(onEvent Handler, phase Phase, leafIndex int, result phaseResult) (bool, error) {
	e.enterPending(phase, leafIndex, result.text, result.toolCalls)
	if err := onEvent(Event{Kind: EventToolCalls, ToolCalls: result.toolCalls}); err != nil {
		return false, err
	}
	return true, nil
}

// clearPending limpia el estado de pausa tras completar una fase.
func (e *Engine) clearPending() {
	e.pendingPhase = PhaseNone
	e.pendingConversation = nil
	e.pendingToolCalls = nil
	e.pendingLeafIndex = 0
	e.toolRoundCount = 0
}

// leafPhaseInputs compone el system/user de la fase de ejecución de una hoja.
func (e *Engine) leafPhaseInputs(leaf *task.Node) (string, string, error) {
	system, err := e.composeSystem(prompts.ExecuteAtomicSystem)
	if err != nil {
		return "", "", err
	}
	userText, err := prompts.Render(prompts.ExecuteAtomicUser, map[string]string{
		prompts.PlaceholderGoal:         e.goalCtx.TurnInstruction,
		prompts.PlaceholderPriorContext: e.priorContextOrDefault(),
		prompts.PlaceholderContext:      e.resultsContext("(ninguno todavía)"),
		prompts.PlaceholderTask:         leaf.Description,
	})
	if err != nil {
		return "", "", err
	}
	return system, userText, nil
}

// synthesisPhaseInputs compone el system/user de la fase de síntesis final.
func (e *Engine) synthesisPhaseInputs() (string, string, error) {
	system, err := e.composeSystem(prompts.SynthesisSystem)
	if err != nil {
		return "", "", err
	}
	userText, err := prompts.Render(prompts.SynthesisUser, map[string]string{
		prompts.PlaceholderGoal:         e.goalCtx.TurnInstruction,
		prompts.PlaceholderPriorContext: e.priorContextOrDefault(),
		prompts.PlaceholderContext:      e.resultsContext("(sin resultados)"),
	})
	if err != nil {
		return "", "", err
	}
	return system, userText, nil
}

// resultsContext une los resultados ya calculados como contexto acumulado.
func (e *Engine) resultsContext(empty string) string {
	if len(e.results) == 0 {
		return empty
	}
	return strings.Join(e.results, "\n")
}

// executeTree inicia la Fase 2: ejecuta las hojas atómicas del árbol.
func (e *Engine) executeTree(ctx context.Context, onEvent Handler) (stopped bool, err error) {
	e.results = nil
	e.leaves = task.CollectAtomicLeaves(e.root)
	if err := emitReasoning(onEvent, fmt.Sprintf("Fase 2 de 3. Implemento las %d tareas atómicas.\n\n", len(e.leaves))); err != nil {
		return false, err
	}
	return e.executeFrom(ctx, 0, onEvent)
}

// executeFrom ejecuta las hojas atómicas a partir de startIndex, con el
// resultado de las anteriores como contexto acumulado.
func (e *Engine) executeFrom(ctx context.Context, startIndex int, onEvent Handler) (stopped bool, err error) {
	total := len(e.leaves)
	for index := startIndex; index < total; index++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		leaf := e.leaves[index]
		label := leaf.Description
		if leaf.Depth == 0 {
			label = "la solicitud"
		}
		if err := emitReasoning(onEvent, fmt.Sprintf("Tarea atómica %d/%d: %s\n\n", index+1, total, label)); err != nil {
			return false, err
		}

		system, userText, err := e.leafPhaseInputs(leaf)
		if err != nil {
			return false, err
		}
		e.toolRoundCount = 0
		e.pendingConversation = nil

		result, err := e.runPhase(ctx, system, userText, EventReasoning, nil, onEvent)
		if err != nil {
			return false, err
		}
		if result.paused {
			return e.pausePhase(onEvent, PhaseLeaf, index, result)
		}
		e.recordLeafResult(leaf, result.text)
	}
	return false, nil
}

// resumePhase continúa la fase que quedó pausada esperando resultados de tool,
// agregando la nueva ronda a pendingConversation (append-only) en vez de
// reconstruirla desde cero — así una segunda (o tercera...) ronda de tool
// calls dentro de la misma fase no pierde el historial de las anteriores.
// Funciona igual para una hoja atómica que para la síntesis final.
func (e *Engine) resumePhase(ctx context.Context, toolOutputs map[string]string, onEvent Handler) (stopped bool, err error) {
	phase := e.pendingPhase
	index := e.pendingLeafIndex // solo válido si phase == PhaseLeaf

	for _, toolCall := range e.pendingToolCalls {
		output := toolOutputs[toolCall.ID]
		toolCallID := toolCall.ID
		e.pendingConversation = append(e.pendingConversation, openai.Message{
			Role:       openai.RoleTool,
			Content:    messageContent(output),
			ToolCallID: &toolCallID,
		})
	}

	emitKind := EventContent
	var system, userText string
	if phase == PhaseLeaf {
		leaf := e.leaves[index]
		system, userText, err = e.leafPhaseInputs(leaf)
		emitKind = EventReasoning
	} else {
		system, userText, err = e.synthesisPhaseInputs()
	}
	if err != nil {
		return false, err
	}

	result, err := e.runPhase(ctx, system, userText, emitKind, e.pendingConversation, onEvent)
	if err != nil {
		return false, err
	}
	if result.paused {
		return e.pausePhase(onEvent, phase, index, result)
	}

	e.clearPending()
	if phase != PhaseLeaf {
		return false, nil // síntesis completada
	}
	e.recordLeafResult(e.leaves[index], result.text)
	return e.executeFrom(ctx, index+1, onEvent)
}

// recordLeafResult guarda el resultado de una hoja y lo agrega al contexto
// acumulado de las tareas posteriores.
func (e *Engine) recordLeafResult(leaf *task.Node, resultText string) {
	leaf.Result = resultText
	e.results = append(e.results, fmt.Sprintf("- %s:\n%s", leaf.Description, resultText))
}

// synthesizeFinal ejecuta la Fase 3: genera la respuesta final visible con
// todos los resultados atómicos ya resueltos.
func (e *Engine) synthesizeFinal(ctx context.Context, onEvent Handler) (stopped bool, err error) {
	system, userText, err := e.synthesisPhaseInputs()
	if err != nil {
		return false, err
	}
	e.toolRoundCount = 0
	e.pendingConversation = nil

	result, err := e.runPhase(ctx, system, userText, EventContent, nil, onEvent)
	if err != nil {
		return false, err
	}
	if result.paused {
		return e.pausePhase(onEvent, PhaseSynthesis, 0, result)
	}
	return false, nil
}
