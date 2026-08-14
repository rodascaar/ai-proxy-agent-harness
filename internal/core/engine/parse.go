package engine

import (
	"encoding/json"
	"strings"
)

// decomposition es la clasificación que devuelve la Fase 1.
type decomposition struct {
	Atomic   bool     `json:"atomic"`
	Subtasks []string `json:"subtasks"`
}

// parseDecomposition interpreta la respuesta de la Fase 1. Es tolerante: si
// el texto no es JSON puro, intenta extraer el primer objeto {...} y, si
// tampoco se puede, cae a "atómica" (no fragmentar es más seguro que
// inventar subtareas).
func parseDecomposition(raw string) decomposition {
	var parsed decomposition
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		return parsed
	}
	if block := extractJSONObject(raw); block != nil {
		if err := json.Unmarshal(block, &parsed); err == nil {
			return parsed
		}
	}
	return decomposition{Atomic: true}
}

// extractJSONObject devuelve el primer objeto JSON balanceado {...} de un
// texto, ignorando llaves dentro de strings.
func extractJSONObject(raw string) []byte {
	start := strings.IndexByte(raw, '{')
	if start == -1 {
		return nil
	}
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(raw); index++ {
		ch := raw[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(raw[start : index+1])
			}
		}
	}
	return nil
}
