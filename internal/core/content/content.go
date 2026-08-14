// Package content separa y recompone contenido multimodal de mensajes: el
// texto plano concatenado por un lado y las partes no-texto (imágenes) por
// otro, para poder reinyectarlas en los mensajes que arma el motor.
package content

import "ai-proxy-agent-harness/internal/core/openai"

// Split separa un content en su texto concatenado y las partes no-texto que
// deban reinyectarse aparte. Un content nulo o vacío produce texto vacío y
// cero partes.
func Split(content *openai.Content) (string, []openai.ContentPart) {
	if content == nil {
		return "", nil
	}
	return content.Text, content.Parts
}

// Build recompone un content de salida: string plano si no hay partes extra
// (compatibilidad total con upstreams sin soporte multimodal), o lista de
// content-parts (texto + partes) si las hay.
func Build(text string, extraParts []openai.ContentPart) *openai.Content {
	content := &openai.Content{Text: text}
	if len(extraParts) > 0 {
		content.Parts = append(content.Parts, extraParts...)
	}
	return content
}

// IsTextOnly indica si el content no tiene partes no-texto.
func IsTextOnly(content *openai.Content) bool {
	return content == nil || len(content.Parts) == 0
}
