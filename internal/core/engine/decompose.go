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

// buildTaskTree construye el árbol de tareas desde la instrucción del turno.
func (e *Engine) buildTaskTree(ctx context.Context, onEvent Handler) error {
	e.root = task.NewNode(e.goalCtx.TurnInstruction, 0)
	return e.decomposeNode(ctx, e.root, onEvent)
}

// decomposeNode decide recursivamente si una tarea es atómica o se subdivide,
// hasta alcanzar la profundidad máxima. La Fase 1 nunca recibe tools
// funcionales (mezclar response_format=json_object con tool-calling es frágil
// entre proveedores), pero sí su descripción como texto para que sepa que
// existen y no fragmente una tarea que una sola invocación resolvería.
func (e *Engine) decomposeNode(ctx context.Context, node *task.Node, onEvent Handler) error {
	if node.Depth >= e.opts.MaxDecompositionDepth {
		node.IsAtomic = true
		return nil
	}

	intro := "Pensando en las subtareas iniciales.\n\n"
	if node.Depth != 0 {
		intro = fmt.Sprintf("Ahora para la subtarea %s, hago sus subtareas.\n\n", node.Description)
	}
	if err := emitReasoning(onEvent, intro); err != nil {
		return err
	}

	system, err := e.composeSystem(prompts.DecompositionSystem)
	if err != nil {
		return err
	}
	userText, err := prompts.Render(prompts.DecompositionUser, map[string]string{
		prompts.PlaceholderGoal:         e.goalCtx.TurnInstruction,
		prompts.PlaceholderPriorContext: e.priorContextOrDefault(),
		prompts.PlaceholderTools:        e.toolsSummary(),
		prompts.PlaceholderTask:         node.Description,
	})
	if err != nil {
		return err
	}

	raw, err := e.llm.Complete(ctx, ports.CompleteRequest{
		Model: e.opts.Model,
		Messages: []openai.Message{
			{Role: openai.RoleSystem, Content: messageContent(system)},
			{Role: openai.RoleUser, Content: e.buildUserContent(userText)},
		},
		JSONMode: true,
	})
	if err != nil {
		return fmt.Errorf("decomposing node: %w", err)
	}

	parsed := parseDecomposition(raw)
	subtasks := parsed.Subtasks
	if parsed.Atomic || len(subtasks) == 0 {
		if err := emitReasoning(onEvent, "Es atómica, no se subdivide más.\n\n"); err != nil {
			return err
		}
		node.IsAtomic = true
		return nil
	}

	subtasksList := "- " + strings.Join(subtasks, "\n- ")
	if err := emitReasoning(onEvent, "Subtareas:\n"+subtasksList+"\n\n"); err != nil {
		return err
	}

	for _, subtask := range subtasks {
		child := task.NewNode(subtask, node.Depth+1)
		node.AddChild(child)
		if err := e.decomposeNode(ctx, child, onEvent); err != nil {
			return err
		}
	}
	return nil
}

// priorContextOrDefault devuelve el contexto previo o un marcador cuando no
// existe, para que los templates lean de forma estable.
func (e *Engine) priorContextOrDefault() string {
	if e.goalCtx.PriorContext == "" {
		return "(sin contexto previo)"
	}
	return e.goalCtx.PriorContext
}
