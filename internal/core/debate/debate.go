// Package debate implementa el "speculum": un bucle de crítica y refinamiento
// que mejora el resultado de una tarea atómica usando uno o más modelos.
//
// Con un solo modelo disponible, ese mismo modelo juega ambos roles (crítico y
// refinador): es el patrón Self-Refine. Con dos o más modelos, el crítico es
// un modelo distinto del refinador, de modo que los modelos se vigilan entre
// sí (multi-agent debate). El razonamiento de cada ronda se expone vía un
// callback para que el usuario vea cómo debatieron.
package debate

import (
	"context"
	"fmt"
	"strings"

	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
	"ai-proxy-agent-harness/internal/prompts"
)

// ApprovedMarker es la señal del crítico de que la respuesta no tiene
// problemas. Se compara de forma tolerante (trim + mayúsculas).
const ApprovedMarker = "[APROBADA]"

// Debater orquesta el bucle crítica → refinamiento sobre el puerto ports.LLMRouter.
type Debater struct {
	router  ports.LLMRouter
	primary string
	rounds  int
	// sampling opcional aplicado a las llamadas del debate (temperatura baja
	// y límite de salida, igual que el resto del motor).
	temperature *float64
	maxTokens   *int
}

// New construye el debater. primary es el modelo del run actual (el refinador
// por defecto). rounds es el máximo de rondas de crítica+refinamiento (2-3).
func New(router ports.LLMRouter, primary string, rounds int) *Debater {
	if rounds < 1 {
		rounds = 1
	}
	return &Debater{router: router, primary: primary, rounds: rounds}
}

// WithSampling fija la temperatura y el límite de salida de las llamadas del
// debate, para que la crítica y el refinamiento se comporten de forma tan
// enfocada como el resto del motor.
func (d *Debater) WithSampling(temperature *float64, maxTokens *int) *Debater {
	d.temperature = temperature
	d.maxTokens = maxTokens
	return d
}

// Refine mejora el resultado de una tarea atómica. initial es el texto ya
// producido por la fase de ejecución. onReasoning recibe fragmentos del debate
// (rol, modelo y texto) para exponerlos como reasoning_content.
func (d *Debater) Refine(ctx context.Context, task, initial string, onReasoning func(text string) error) (string, error) {
	current := initial
	criticModel := d.pickCritic()

	for round := 1; round <= d.rounds; round++ {
		critique, err := d.critique(ctx, criticModel, task, current)
		if err != nil {
			return current, err
		}
		if onReasoning != nil {
			label := fmt.Sprintf("[Debate ronda %d — crítico %s]\n%s\n\n", round, criticModel, critique)
			if err := onReasoning(label); err != nil {
				return current, err
			}
		}
		if isApproved(critique) {
			if onReasoning != nil {
				_ = onReasoning(fmt.Sprintf("[Debate ronda %d] El crítico aprobó la respuesta; se conserva sin cambios.\n\n", round))
			}
			return current, nil
		}

		refined, err := d.refine(ctx, task, current, critique)
		if err != nil {
			return current, err
		}
		if onReasoning != nil {
			label := fmt.Sprintf("[Debate ronda %d — refinador %s]\n%s\n\n", round, d.primary, refined)
			if err := onReasoning(label); err != nil {
				return current, err
			}
		}
		current = refined
	}
	return current, nil
}

// pickCritic elige el modelo crítico: el primer modelo distinto del primario
// (si lo hay) o el primario si solo hay uno disponible.
func (d *Debater) pickCritic() string {
	for _, model := range d.router.AllModels() {
		if model != d.primary {
			return model
		}
	}
	return d.primary
}

// critique pide al crítico que evalúe la respuesta de la tarea.
func (d *Debater) critique(ctx context.Context, criticModel, task, response string) (string, error) {
	system, err := prompts.Render(prompts.RefineCriticSystem, nil)
	if err != nil {
		return "", err
	}
	user, err := prompts.Render(prompts.RefineCriticUser, map[string]string{
		prompts.PlaceholderTask:     task,
		prompts.PlaceholderResponse: response,
	})
	if err != nil {
		return "", err
	}
	return d.complete(ctx, criticModel, system, user)
}

// refine pide al refinador que corrija la respuesta usando la crítica.
func (d *Debater) refine(ctx context.Context, task, response, critique string) (string, error) {
	system, err := prompts.Render(prompts.RefineRefineSystem, nil)
	if err != nil {
		return "", err
	}
	user, err := prompts.Render(prompts.RefineRefineUser, map[string]string{
		prompts.PlaceholderTask:     task,
		prompts.PlaceholderResponse: response,
		prompts.PlaceholderCritique: critique,
	})
	if err != nil {
		return "", err
	}
	return d.complete(ctx, d.primary, system, user)
}

// complete hace una llamada no-streaming al modelo indicado, devolviendo el
// contenido de texto. Aísla la construcción de mensajes.
func (d *Debater) complete(ctx context.Context, model, system, user string) (string, error) {
	client, err := d.router.ClientFor(model)
	if err != nil {
		return "", err
	}
	content, err := client.Complete(ctx, ports.CompleteRequest{
		Model: model,
		Messages: []openai.Message{
			{Role: openai.RoleSystem, Content: openai.NewTextContent(system)},
			{Role: openai.RoleUser, Content: openai.NewTextContent(user)},
		},
		Temperature: d.temperature,
		MaxTokens:   d.maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("debate complete (%s): %w", model, err)
	}
	return content, nil
}

// isApproved detecta la marca de aprobación del crítico de forma tolerante.
func isApproved(critique string) bool {
	return strings.TrimSpace(strings.ToUpper(critique)) == ApprovedMarker
}
