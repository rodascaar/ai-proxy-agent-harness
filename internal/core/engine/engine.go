// Package engine implementa el motor de descomposición atómica: orquesta las
// tres fases —planificar, ejecutar hojas atómicas, sintetizar— sobre un
// upstream LLM expuesto a través del puerto ports.LLMClient.
//
// El motor es resumible: si una fase requiere tool_calls, emite un evento
// EventToolCalls y queda pausado (estado en pending*). Una sesión persistida
// puede restaurarlo y continuar exactamente donde quedó sin rehacer trabajo.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ai-proxy-agent-harness/internal/core/content"
	"ai-proxy-agent-harness/internal/core/goal"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
	"ai-proxy-agent-harness/internal/core/task"
	"ai-proxy-agent-harness/internal/prompts"
)

// Phase identifica la fase que quedó pausada esperando resultados de tool.
type Phase int

// Fases del motor.
const (
	PhaseNone Phase = iota
	PhaseLeaf
	PhaseSynthesis
)

// String devuelve la representación textual de la fase.
func (p Phase) String() string {
	switch p {
	case PhaseLeaf:
		return "leaf"
	case PhaseSynthesis:
		return "synthesis"
	default:
		return "none"
	}
}

// MarshalJSON serializa la fase como string para que el estado persistido de
// una sesión sea estable y legible.
func (p Phase) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.String())
}

// UnmarshalJSON reconstruye la fase desde su representación textual.
func (p *Phase) UnmarshalJSON(b []byte) error {
	var value string
	if err := json.Unmarshal(b, &value); err != nil {
		return fmt.Errorf("unmarshal phase: %w", err)
	}
	switch value {
	case "leaf":
		*p = PhaseLeaf
	case "synthesis":
		*p = PhaseSynthesis
	case "none":
		*p = PhaseNone
	default:
		return fmt.Errorf("unknown phase %q", value)
	}
	return nil
}

// EventKind clasifica los eventos que el motor emite hacia el orquestador.
type EventKind int

// Tipos de evento emitidos por el motor.
const (
	EventReasoning EventKind = iota
	EventContent
	EventToolCalls
)

// Event es un fragmento de la ejecución del motor: texto de razonamiento,
// texto de contenido visible o una petición de tool_calls.
type Event struct {
	Kind      EventKind
	Text      string
	ToolCalls []openai.ToolCall
}

// Handler recibe los eventos del motor mientras este se ejecuta. Si devuelve
// un error, el motor aborta y lo propaga (permite cortar el streaming cuando
// el cliente se desconecta).
type Handler func(Event) error

// Options son los parámetros de construcción del motor.
type Options struct {
	Model                 string
	Tools                 []openai.Tool
	ToolChoice            json.RawMessage
	MaxDecompositionDepth int
	MaxToolRoundsPerPhase int
}

// Engine orquesta las tres fases de descomposición atómica. El estado de
// pausa/reanudación se expone a través de getters para que la capa de
// aplicación pueda persistirlo en una sesión.
type Engine struct {
	llm     ports.LLMClient
	opts    Options
	goalCtx *goal.Context

	root    *task.Node
	leaves  []*task.Node
	results []string

	pendingPhase        Phase
	pendingLeafIndex    int
	pendingToolCalls    []openai.ToolCall
	pendingConversation []openai.Message
	toolRoundCount      int
}

// New construye un motor. maxDepth y maxToolRounds deben ser >= 1 (se
// validan en config), pero por seguridad se corrigen aquí al mínimo válido.
func New(llm ports.LLMClient, opts Options) *Engine {
	if opts.MaxDecompositionDepth < 1 {
		opts.MaxDecompositionDepth = 1
	}
	if opts.MaxToolRoundsPerPhase < 1 {
		opts.MaxToolRoundsPerPhase = 1
	}
	return &Engine{llm: llm, opts: opts}
}

// SetGoalContext fija el contexto del turno externo antes de Run() o Resume().
func (e *Engine) SetGoalContext(ctx *goal.Context) {
	e.goalCtx = ctx
}

// RestoreTree restaura el árbol de tareas y los resultados ya calculados
// desde una sesión persistida.
func (e *Engine) RestoreTree(root *task.Node, leaves []*task.Node, results []string) {
	e.root = root
	e.leaves = leaves
	e.results = results
}

// RestorePending restaura el estado de pausa/reanudación desde una sesión
// persistida.
func (e *Engine) RestorePending(phase Phase, leafIndex int, toolCalls []openai.ToolCall, conversation []openai.Message, toolRoundCount int) {
	e.pendingPhase = phase
	e.pendingLeafIndex = leafIndex
	e.pendingToolCalls = toolCalls
	e.pendingConversation = conversation
	e.toolRoundCount = toolRoundCount
}

// Run ejecuta el ciclo completo de tres fases para el turno actual.
func (e *Engine) Run(ctx context.Context, onEvent Handler) error {
	if e.goalCtx == nil {
		return errors.New("engine: goal context is not set")
	}
	if err := emitReasoning(onEvent, "Fase 1 de 3. Primero comienzo dividiendo la tarea en sus subtareas atómicas.\n\n"); err != nil {
		return err
	}
	if err := e.buildTaskTree(ctx, onEvent); err != nil {
		return err
	}
	if err := emitReasoning(onEvent, "Listo, tenemos la lista completa del árbol de tareas hasta sus subtareas atómicas.\n\n"); err != nil {
		return err
	}
	if treeLines := task.RenderTree(e.root); treeLines != "" {
		if err := emitReasoning(onEvent, treeLines+"\n\n"); err != nil {
			return err
		}
	}

	stopped, err := e.executeTree(ctx, onEvent)
	if err != nil {
		return err
	}
	if stopped {
		return nil
	}

	if err := emitReasoning(onEvent, "Fase 3 de 3. Listo, todas las tareas atómicas trabajadas correctamente, procedo a dar la respuesta final.\n"); err != nil {
		return err
	}
	_, err = e.synthesizeFinal(ctx, onEvent)
	return err
}

// Resume continúa un Run() previamente pausado por una tool call (en una hoja
// o en la síntesis), sin volver a descomponer el objetivo ni reejecutar
// trabajo ya resuelto.
func (e *Engine) Resume(ctx context.Context, toolOutputs map[string]string, onEvent Handler) error {
	if e.pendingPhase == PhaseNone {
		return errors.New("engine: cannot resume, no pending phase")
	}
	resumingPhase := e.pendingPhase

	stopped, err := e.resumePhase(ctx, toolOutputs, onEvent)
	if err != nil {
		return err
	}
	if stopped {
		return nil
	}
	if resumingPhase == PhaseSynthesis {
		return nil // resumePhase ya completó la síntesis
	}

	if err := emitReasoning(onEvent, "Fase 3 de 3. Listo, todas las tareas atómicas trabajadas correctamente, procedo a dar la respuesta final.\n"); err != nil {
		return err
	}
	_, err = e.synthesizeFinal(ctx, onEvent)
	return err
}

// ---------------------------------------------------------------------------
// Accesores de estado para la persistencia de sesiones (capa de aplicación).
// ---------------------------------------------------------------------------

// Model devuelve el modelo configurado del run.
func (e *Engine) Model() string { return e.opts.Model }

// Tools devuelve las herramientas funcionales del caller.
func (e *Engine) Tools() []openai.Tool { return e.opts.Tools }

// ToolChoice devuelve el tool_choice original del caller.
func (e *Engine) ToolChoice() json.RawMessage { return e.opts.ToolChoice }

// GoalContext devuelve el contexto de objetivo del turno.
func (e *Engine) GoalContext() *goal.Context { return e.goalCtx }

// Root devuelve el nodo raíz del árbol de tareas.
func (e *Engine) Root() *task.Node { return e.root }

// Leaves devuelve las hojas atómicas del árbol.
func (e *Engine) Leaves() []*task.Node { return e.leaves }

// Results devuelve los resultados de las hojas ejecutadas.
func (e *Engine) Results() []string { return e.results }

// PendingPhase devuelve la fase en la que el run quedó pausado.
func (e *Engine) PendingPhase() Phase { return e.pendingPhase }

// PendingLeafIndex devuelve el índice de la hoja pendiente (si aplica).
func (e *Engine) PendingLeafIndex() int { return e.pendingLeafIndex }

// PendingToolCalls devuelve las tool calls pendientes de la pausa.
func (e *Engine) PendingToolCalls() []openai.ToolCall { return e.pendingToolCalls }

// PendingConversation devuelve la conversación interna al momento de la pausa.
func (e *Engine) PendingConversation() []openai.Message { return e.pendingConversation }

// ToolRoundCount devuelve la cantidad de rondas de tools ya agotadas.
func (e *Engine) ToolRoundCount() int { return e.toolRoundCount }

// ---------------------------------------------------------------------------
// Helpers de composición de prompts
// ---------------------------------------------------------------------------

// composeSystem antepone el system prompt real del caller (si lo hay) como una
// capa de autoridad sobre el prompt interno de esta fase, en vez de
// sustituirlo — así el caller (p. ej. un agente de código) no pierde el
// control sobre el comportamiento del modelo en cada llamada interna.
func (e *Engine) composeSystem(phasePromptName string) (string, error) {
	phasePrompt, err := prompts.Render(phasePromptName, nil)
	if err != nil {
		return "", err
	}
	if e.goalCtx == nil || e.goalCtx.CallerSystem == "" {
		return phasePrompt, nil
	}
	preamble, err := prompts.Render(prompts.CallerSystemPreamble, map[string]string{
		prompts.PlaceholderCallerSystem: e.goalCtx.CallerSystem,
	})
	if err != nil {
		return "", err
	}
	return preamble + "\n\n" + phasePrompt, nil
}

// buildUserContent recompone el content del mensaje user: texto plano si no
// hay imágenes, o texto + partes de imagen si las hay.
func (e *Engine) buildUserContent(text string) *openai.Content {
	var imageParts []openai.ContentPart
	if e.goalCtx != nil {
		imageParts = e.goalCtx.ImageParts
	}
	return content.Build(text, imageParts)
}

// toolsSummary describe textualmente (no funcionalmente) las tools
// disponibles, para que la Fase 1 sepa que existen sin recibirlas como tools
// reales — así evita el defecto de clasificar como "no atómica" cualquier
// tarea que una sola invocación de herramienta resolvería.
func (e *Engine) toolsSummary() string {
	if len(e.opts.Tools) == 0 {
		return "(ninguna herramienta disponible)"
	}
	lines := make([]string, 0, len(e.opts.Tools))
	for _, tool := range e.opts.Tools {
		name := tool.Function.Name
		if tool.Function.Description != "" {
			lines = append(lines, "- "+name+": "+tool.Function.Description)
		} else {
			lines = append(lines, "- "+name)
		}
	}
	return strings.Join(lines, "\n")
}

// normalizeToolChoice aplica la política única de tools/tool_choice para
// todas las llamadas internas: si el caller no dio tools (o las desactivó con
// "none"), no se ofrecen; si las dio, siempre se ofrecen en modo "auto" —
// nunca se fuerza un tool_choice específico del caller en las tareas internas.
func (e *Engine) normalizeToolChoice() ([]openai.Tool, json.RawMessage) {
	if len(e.opts.Tools) == 0 || openai.ToolChoiceIsNone(e.opts.ToolChoice) {
		return nil, nil
	}
	return e.opts.Tools, json.RawMessage(`"auto"`)
}

func emitReasoning(onEvent Handler, text string) error {
	return onEvent(Event{Kind: EventReasoning, Text: text})
}

func messageContent(text string) *openai.Content {
	if text == "" {
		return nil
	}
	return &openai.Content{Text: text}
}
