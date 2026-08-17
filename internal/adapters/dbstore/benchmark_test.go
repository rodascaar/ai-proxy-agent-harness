package dbstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"ai-proxy-agent-harness/internal/core/openai"
)

// benchMessages arma un historial sintético de n turnos (user + assistant).
func benchMessages(turns int) []openai.Message {
	messages := make([]openai.Message, 0, turns*2)
	for i := 0; i < turns; i++ {
		messages = append(messages, openai.Message{
			Role:    openai.RoleUser,
			Content: openai.NewTextContent(fmt.Sprintf("pregunta número %d sobre un tema cualquiera", i)),
		})
		messages = append(messages, openai.Message{
			Role:    openai.RoleAssistant,
			Content: openai.NewTextContent(fmt.Sprintf("respuesta larga número %d con contexto útil", i)),
		})
	}
	return messages
}

func benchStore(b *testing.B) *Store {
	b.Helper()
	store, err := Open(filepath.Join(b.TempDir(), "proxy.db"), time.Hour, 10000, nil)
	if err != nil {
		b.Fatalf("Open(): %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	return store
}

// BenchmarkSaveSession mide el write-through de una sesión completa a SQLite
// (upsert + evicción TTL + conteo de overflow en la misma transacción).
func BenchmarkSaveSession(b *testing.B) {
	store := benchStore(b)
	state := sampleState(b, benchMessages(8))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Save(context.Background(), state); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFindMatching mide la resolución de una reanudación (query indexada
// con la cadena de checkpoints materializada como tabla VALUES).
func BenchmarkFindMatching(b *testing.B) {
	store := benchStore(b)
	messages := benchMessages(20)
	for i := 0; i < 100; i++ {
		state := sampleState(b, messages)
		state.SessionID = fmt.Sprintf("session-%d", i)
		if err := store.Save(context.Background(), state); err != nil {
			b.Fatal(err)
		}
	}
	resume := append(append([]openai.Message{}, messages...),
		openai.Message{Role: openai.RoleTool, ToolCallID: &resumeToolCallID, Content: openai.NewTextContent("salida de la herramienta")})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.FindMatching(context.Background(), resume); err != nil {
			b.Fatal(err)
		}
	}
}

var resumeToolCallID = "call_benchmark"

// BenchmarkAppendMessage mide el append de un turno a una conversación
// existente (upsert de la conversación + inserción de mensajes en tx).
func BenchmarkAppendMessage(b *testing.B) {
	store := benchStore(b)
	ctx := context.Background()
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	turn := []openai.Message{
		{Role: openai.RoleUser, Content: openai.NewTextContent("otra pregunta del usuario")},
		{Role: openai.RoleAssistant, Content: openai.NewTextContent("y otra respuesta del modelo")},
	}
	if _, err := store.Append(ctx, id, "test-model", turn); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Append(ctx, id, "test-model", turn); err != nil {
			b.Fatal(err)
		}
	}
}
