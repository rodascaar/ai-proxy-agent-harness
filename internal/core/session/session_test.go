package session

import (
	"testing"

	"ai-proxy-agent-harness/internal/core/engine"
	"ai-proxy-agent-harness/internal/core/openai"
)

func toolResultMessage(id, text string) openai.Message {
	return openai.Message{Role: openai.RoleTool, ToolCallID: &id, Content: openai.NewTextContent(text)}
}

func TestHashChainIsStableAndPrefixStable(t *testing.T) {
	base := []openai.Message{
		{Role: openai.RoleSystem, Content: openai.NewTextContent("sistema")},
		{Role: openai.RoleUser, Content: openai.NewTextContent("hola")},
		{Role: openai.RoleAssistant, Content: openai.NewTextContent("respuesta")},
	}
	chain1, err := HashChain(base)
	if err != nil {
		t.Fatalf("HashChain() error: %v", err)
	}
	if len(chain1) != 3 {
		t.Fatalf("expected chain len 3, got %d", len(chain1))
	}

	chain2, err := HashChain(base)
	if err != nil {
		t.Fatalf("HashChain() error: %v", err)
	}
	for i := range base {
		if chain1[i] != chain2[i] {
			t.Errorf("chain[%d] not stable across identical inputs", i)
		}
	}

	prefixed := append(append([]openai.Message{}, base...), toolResultMessage("call_1", "salida"))
	chain3, err := HashChain(prefixed)
	if err != nil {
		t.Fatalf("HashChain() error: %v", err)
	}
	if len(chain3) != 4 {
		t.Fatalf("expected chain len 4, got %d", len(chain3))
	}
	for i := range base {
		if chain1[i] != chain3[i] {
			t.Errorf("chain[%d] should be identical for the common prefix", i)
		}
	}

	tampered := []openai.Message{
		base[0],
		{Role: openai.RoleUser, Content: openai.NewTextContent("hola (editado)")},
		base[2],
	}
	chain4, err := HashChain(tampered)
	if err != nil {
		t.Fatalf("HashChain() error: %v", err)
	}
	if chain4[1] == chain1[1] {
		t.Errorf("hash should change when a message changes")
	}
}

func TestHashChainChangesWithMessageContent(t *testing.T) {
	a := []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("x")}}
	b := []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("y")}}
	ca, _ := HashChain(a)
	cb, _ := HashChain(b)
	if ca[0] == cb[0] {
		t.Errorf("different content must produce different hashes")
	}
}

func TestIsValidResume(t *testing.T) {
	base := []openai.Message{
		{Role: openai.RoleSystem, Content: openai.NewTextContent("s")},
		{Role: openai.RoleUser, Content: openai.NewTextContent("usa la herramienta")},
	}
	baseLen := len(base)

	cases := []struct {
		name     string
		state    State
		messages []openai.Message
		want     bool
	}{
		{
			name: "valid leaf resume",
			state: State{
				CheckpointLen: baseLen,
				PendingPhase:  engine.PhaseLeaf,
				PendingToolCalls: []openai.ToolCall{{
					ID: "call_1", Type: "function",
					Function: openai.FunctionCall{Name: "leer", Arguments: "{}"},
				}},
			},
			messages: append(append([]openai.Message{}, base...), toolResultMessage("call_1", "ok")),
			want:     true,
		},
		{
			name: "missing tool output is invalid",
			state: State{
				CheckpointLen: baseLen,
				PendingPhase:  engine.PhaseLeaf,
				PendingToolCalls: []openai.ToolCall{{
					ID: "call_1", Type: "function",
					Function: openai.FunctionCall{Name: "leer", Arguments: "{}"},
				}},
			},
			messages: append(append([]openai.Message{}, base...), toolResultMessage("call_otro", "no")),
			want:     false,
		},
		{
			name: "messages shorter than checkpoint is invalid",
			state: State{
				CheckpointLen: baseLen,
				PendingPhase:  engine.PhaseLeaf,
				PendingToolCalls: []openai.ToolCall{{
					ID: "call_1", Type: "function",
					Function: openai.FunctionCall{Name: "leer", Arguments: "{}"},
				}},
			},
			messages: base[:baseLen-1],
			want:     false,
		},
		{
			name: "no pending phase is invalid",
			state: State{
				CheckpointLen: baseLen,
				PendingPhase:  engine.PhaseNone,
				PendingToolCalls: []openai.ToolCall{{
					ID: "call_1", Type: "function",
					Function: openai.FunctionCall{Name: "leer", Arguments: "{}"},
				}},
			},
			messages: append(append([]openai.Message{}, base...), toolResultMessage("call_1", "ok")),
			want:     false,
		},
		{
			name: "no pending tool calls is invalid",
			state: State{
				CheckpointLen: baseLen,
				PendingPhase:  engine.PhaseLeaf,
			},
			messages: append(append([]openai.Message{}, base...), toolResultMessage("call_1", "ok")),
			want:     false,
		},
		{
			name: "all pending calls must have outputs",
			state: State{
				CheckpointLen: baseLen,
				PendingPhase:  engine.PhaseLeaf,
				PendingToolCalls: []openai.ToolCall{
					{ID: "call_1", Type: "function", Function: openai.FunctionCall{Name: "a", Arguments: "{}"}},
					{ID: "call_2", Type: "function", Function: openai.FunctionCall{Name: "b", Arguments: "{}"}},
				},
			},
			messages: append(append([]openai.Message{}, base...), toolResultMessage("call_1", "ok")),
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidResume(&tc.state, tc.messages); got != tc.want {
				t.Errorf("IsValidResume() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsNewTurn(t *testing.T) {
	base := []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("q")}}
	finished := State{CheckpointLen: len(base), PendingPhase: engine.PhaseNone}
	if !IsNewTurn(&finished, append(append([]openai.Message{}, base...), openai.Message{Role: openai.RoleUser, Content: openai.NewTextContent("siguiente")})) {
		t.Errorf("expected new turn when phase is none and messages grew")
	}
	if IsNewTurn(&finished, base) {
		t.Errorf("no new turn when messages are unchanged")
	}

	pending := State{CheckpointLen: len(base), PendingPhase: engine.PhaseLeaf}
	if IsNewTurn(&pending, append(append([]openai.Message{}, base...), toolResultMessage("c", "o"))) {
		t.Errorf("a resume is not a new turn")
	}
}

func TestExtractToolOutputs(t *testing.T) {
	base := []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("q")}}
	baseLen := len(base)
	state := State{
		CheckpointLen: baseLen,
		PendingPhase:  engine.PhaseLeaf,
		PendingToolCalls: []openai.ToolCall{
			{ID: "call_1", Type: "function", Function: openai.FunctionCall{Name: "a", Arguments: "{}"}},
			{ID: "call_2", Type: "function", Function: openai.FunctionCall{Name: "b", Arguments: "{}"}},
		},
	}
	messages := append(append([]openai.Message{}, base...),
		toolResultMessage("call_1", "salida uno"),
		openai.Message{Role: openai.RoleUser, Content: openai.NewTextContent("mensaje intercalado")},
		toolResultMessage("call_ignorado", "no pendiente"),
		toolResultMessage("call_2", "salida dos"),
	)

	outputs := ExtractToolOutputs(&state, messages)
	if len(outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d: %#v", len(outputs), outputs)
	}
	if outputs["call_1"] != "salida uno" || outputs["call_2"] != "salida dos" {
		t.Errorf("wrong outputs: %#v", outputs)
	}
	if _, ok := outputs["call_ignorado"]; ok {
		t.Errorf("non-pending call id must not be extracted")
	}
}

func TestPendingToolCallIDs(t *testing.T) {
	state := State{PendingToolCalls: []openai.ToolCall{
		{ID: "a"}, {ID: "b"}, {ID: ""},
	}}
	ids := state.PendingToolCallIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 ids, got %d", len(ids))
	}
	if _, ok := ids["a"]; !ok {
		t.Errorf("missing id a")
	}
	if _, ok := ids["b"]; !ok {
		t.Errorf("missing id b")
	}
}
