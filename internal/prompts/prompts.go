// Package prompts expone los prompts de cada fase del motor de descomposición
// atómica. Los archivos .md se embeben en el binario con go:embed.
//
// Los templates usan placeholders de la forma {nombre}. El renderizado es un
// reemplazo textual seguro: los valores se insertan como texto plano y nunca
// se interpretan como instrucciones de template.
package prompts

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed *.md
var promptFiles embed.FS

// Nombres de los templates, documentados para no esparcir literales.
const (
	DecompositionSystem  = "decomposition_system.md"
	DecompositionUser    = "decomposition_user.md"
	ExecuteAtomicSystem  = "execute_atomic_system.md"
	ExecuteAtomicUser    = "execute_atomic_user.md"
	SynthesisSystem      = "synthesis_system.md"
	SynthesisUser        = "synthesis_user.md"
	CallerSystemPreamble = "caller_system_preamble.md"
	RefineCriticSystem   = "refine_critic_system.md"
	RefineCriticUser     = "refine_critic_user.md"
	RefineRefineSystem   = "refine_refine_system.md"
	RefineRefineUser     = "refine_refine_user.md"
)

// Placeholders soportados por Render.
const (
	PlaceholderGoal         = "{goal}"
	PlaceholderPriorContext = "{prior_context}"
	PlaceholderTools        = "{tools}"
	PlaceholderTask         = "{task}"
	PlaceholderContext      = "{context}"
	PlaceholderCallerSystem = "{caller_system}"
	PlaceholderResponse     = "{response}"
	PlaceholderCritique     = "{critique}"
)

// Render lee el template indicado y sustituye sus placeholders. Los valores
// se tratan como texto plano; un placeholder desconocido en el archivo se
// deja intacto (es un bug del template, no del llamador).
func Render(name string, values map[string]string) (string, error) {
	raw, err := promptFiles.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("reading prompt %s: %w", name, err)
	}
	return replacePlaceholders(string(raw), values), nil
}

func replacePlaceholders(template string, values map[string]string) string {
	replacer := make([]string, 0, len(values)*2)
	for key, value := range values {
		replacer = append(replacer, key, value)
	}
	return strings.NewReplacer(replacer...).Replace(template)
}
