package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"ai-proxy-agent-harness/internal/core/debate"
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

	phaseLabel := "synthesis"
	if emitKind == EventReasoning {
		phaseLabel = "leaf"
	}
	e.logPromptSizes(phaseLabel, system, userText)
	temp, maxTokens := e.sampling()
	err := e.llm.Stream(ctx, ports.StreamRequest{
		Model:       e.opts.Model,
		Messages:    messages,
		Tools:       phaseTools,
		ToolChoice:  phaseToolChoice,
		Temperature: temp,
		MaxTokens:   maxTokens,
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
		prompts.PlaceholderTools:        e.toolsSummary(),
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
		prompts.PlaceholderTools:        e.toolsSummary(),
	})
	if err != nil {
		return "", "", err
	}
	return system, userText, nil
}

// resultsContext une los resultados ya calculados como contexto acumulado,
// podado a un presupuesto total (cabeza+cola) para que una larga cadena de
// hojas no haga perder el foco al modelo.
func (e *Engine) resultsContext(empty string) string {
	if len(e.results) == 0 {
		return empty
	}
	joined := strings.Join(e.results, "\n")
	return trimContext(joined, maxResultsContextRunes)
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
		result.text = e.correctNeedsToolMarker(ctx, system, userText, result.text, onEvent)
		e.recordLeafResult(leaf, e.debateResult(ctx, leaf.Description, result.text, onEvent))
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
	result.text = e.correctNeedsToolMarker(ctx, system, userText, result.text, onEvent)
	e.recordLeafResult(e.leaves[index], e.debateResult(ctx, e.leaves[index].Description, result.text, onEvent))
	return e.executeFrom(ctx, index+1, onEvent)
}

// debateResult aplica el speculum sobre el resultado de una tarea atómica si
// el debate está activado. Si está desactivado (o no hay router), devuelve el
// texto sin cambios. Un error del debate NO aborta el run: conserva el texto
// original (el debate es una mejora, no una dependencia crítica).
func (e *Engine) debateResult(ctx context.Context, task, text string, onEvent Handler) string {
	if e.opts.Debate == nil || !e.opts.Debate.Enabled || e.opts.Debate.Router == nil || text == "" {
		return text
	}
	if err := emitReasoning(onEvent, "\n\n[Speculum] Someto el resultado a crítica y refinamiento.\n\n"); err != nil {
		return text
	}
	debater := debate.New(e.opts.Debate.Router, e.opts.Model, e.opts.Debate.Rounds)
	temp, maxTokens := e.sampling()
	debater.WithSampling(temp, maxTokens)
	refined, err := debater.Refine(ctx, task, text, func(reasoning string) error {
		return emitReasoning(onEvent, reasoning)
	})
	if err != nil {
		// Conservamos el original y seguimos: el debate falló, no el run.
		_ = emitReasoning(onEvent, "[Speculum] No se pudo completar el debate; se conserva el resultado original.\n\n")
		return text
	}
	return refined
}

// recordLeafResult guarda el resultado de una hoja y lo agrega al contexto
// acumulado de las tareas posteriores. El resultado se sanitiza contra el
// marcador [[NECESITA_HERRAMIENTA]] cuando no hay tools (defensa en
// profundidad; la corrección principal ocurre en correctNeedsToolMarker).
func (e *Engine) recordLeafResult(leaf *task.Node, resultText string) {
	leaf.Result = resultText
	e.results = append(e.results, fmt.Sprintf("- %s:\n%s", leaf.Description, e.sanitizeResultForContext(resultText)))
}

// sanitizeResultForContext reemplaza marcadores [[NECESITA_HERRAMIENTA]] por
// una nota de pendiente honesta solo cuando NO hay tools reales: si las hay,
// la síntesis mantiene el protocolo <pendientes_marcados> para resolverlas.
func (e *Engine) sanitizeResultForContext(text string) string {
	if len(e.opts.Tools) > 0 {
		return text
	}
	return sanitizeNeedsToolMarker(text)
}

// needsToolMarkerRegex reconoce el marcador [[NECESITA_HERRAMIENTA: descripción]]
// que el modelo usa para reportar una subtarea que requiere una acción externa.
var needsToolMarkerRegex = regexp.MustCompile(`\[\[\s*NECESITA_HERRAMIENTA\s*:?\s*([^\]]*)\]\]`)

// extractNeedsToolDescription devuelve la descripción del primer marcador
// [[NECESITA_HERRAMIENTA: ...]] presente en el texto, o "" si no hay ninguno.
func extractNeedsToolDescription(text string) string {
	m := needsToolMarkerRegex.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// needsToolFallback es la nota honesta que reemplaza un resultado de hoja que
// quedó dependiendo de una herramienta no disponible: nunca fabrica contenido.
func needsToolFallback(description string) string {
	if description == "" {
		description = "una acción externa no disponible"
	}
	return "[Pendiente: esta subtarea requiere " + description + ", pero no hay una herramienta disponible para realizarla.]"
}

// sanitizeNeedsToolMarker reemplaza cualquier marcador [[NECESITA_HERRAMIENTA]]
// remanente en un texto por su nota de pendiente honesta.
func sanitizeNeedsToolMarker(text string) string {
	return needsToolMarkerRegex.ReplaceAllStringFunc(text, func(match string) string {
		return needsToolFallback(extractNeedsToolDescription(match))
	})
}

// correctNeedsToolMarker corrige el resultado de una hoja si el modelo emitió
// el marcador [[NECESITA_HERRAMIENTA: ...]] sin que el caller tenga
// herramientas: es una desviación del modelo (alucinación) y no debe llegar a
// la síntesis como si fuera un resultado real. Reintenta la hoja UNA vez con
// una corrección explícita para que responda directamente; si el marcador
// persiste tras el reintento, devuelve una nota honesta de pendiente.
func (e *Engine) correctNeedsToolMarker(ctx context.Context, system, userText, resultText string, onEvent Handler) string {
	description := extractNeedsToolDescription(resultText)
	if description == "" || len(e.opts.Tools) > 0 {
		return resultText // sin marcador, o protocolo legítimo con tools del caller
	}
	if err := emitReasoning(onEvent, "\n\n[No hay herramientas disponibles y el modelo marcó la tarea como dependiente de una herramienta. Reintento la hoja pidiendo respuesta directa.]\n\n"); err != nil {
		return needsToolFallback(description)
	}

	correction := openai.Message{
		Role: openai.RoleUser,
		Content: messageContent("CORRECCIÓN: no tienes herramientas disponibles en esta llamada. Responde la tarea " +
			"atómica directamente con tu conocimiento, sin simular acciones externas ni usar el formato " +
			"[[NECESITA_HERRAMIENTA]]. Si realmente no puedes responderla sin una herramienta, dilo en una línea breve y honesta."),
	}
	retry, err := e.runPhase(ctx, system, userText, EventReasoning, []openai.Message{correction}, onEvent)
	if err != nil {
		return needsToolFallback(description)
	}
	if retry.paused {
		// Sin tools ofrecidas el upstream no debería devolver tool_calls nativas;
		// si lo hace, respetamos el protocolo conservando el resultado original.
		return resultText
	}
	if extractNeedsToolDescription(retry.text) != "" {
		return needsToolFallback(description)
	}
	return retry.text
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
