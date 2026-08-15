package service_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"ai-proxy-agent-harness/internal/adapters/sessionstore/md"
	"ai-proxy-agent-harness/internal/application/service"
	"ai-proxy-agent-harness/internal/core/engine"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
	"ai-proxy-agent-harness/internal/testutil/fakellm"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newService(t *testing.T, llm ports.LLMClient) (*service.Service, *md.Store) {
	t.Helper()
	store, err := md.New(t.TempDir(), time.Minute, 100, noopLogger())
	if err != nil {
		t.Fatalf("md.New() error: %v", err)
	}
	svc := service.New(llm, store, "test-model", 3, 25, noopLogger())
	return svc, store
}

func atomicCompletion() string {
	return `{"atomic": true, "subtasks": []}`
}

func requestWith(messages []openai.Message) *openai.ChatCompletionRequest {
	return &openai.ChatCompletionRequest{Messages: messages}
}

func stringPtr(value string) *string {
	return &value
}

// runPausing ejecuta un run que queda pausado por una tool call y lo persiste.
func runPausing(t *testing.T, svc *service.Service, first []openai.Message) {
	t.Helper()
	run, err := svc.Prepare(requestWith(first))
	if err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}
	var paused bool
	if err := svc.Consume(context.Background(), run, func(ev engine.Event) error {
		if ev.Kind == engine.EventToolCalls {
			paused = true
		}
		return nil
	}); err != nil {
		t.Fatalf("Consume() error: %v", err)
	}
	if !paused {
		t.Fatalf("expected the run to pause on a tool call")
	}
	if err := svc.Persist(run, true, ""); err != nil {
		t.Fatalf("Persist() error: %v", err)
	}
	run.Release()
}

func toolCallMessages(toolCallID, toolResult string) []openai.Message {
	return []openai.Message{
		{Role: openai.RoleUser, Content: openai.NewTextContent("usa la herramienta")},
		{Role: openai.RoleAssistant, ToolCalls: []openai.ToolCall{{
			ID: "call_1", Type: "function",
			Function: openai.FunctionCall{Name: "leer", Arguments: "{}"},
		}}},
		{Role: openai.RoleTool, ToolCallID: stringPtr(toolCallID), Content: openai.NewTextContent(toolResult)},
	}
}

func TestPreparePinsModel(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"ok"}, nil).
		StreamResponse([]string{"final"}, nil)
	svc, _ := newService(t, llm)

	// El cliente pide un modelo distinto; el proxy debe pinearlo a
	// UPSTREAM_MODEL (default) para no recargar modelos en el upstream.
	req := requestWith([]openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("haz algo")}})
	req.Model = stringPtr("otro-modelo-distinto")

	run, err := svc.Prepare(req)
	if err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}
	if run.Model != "test-model" {
		t.Errorf("run model should be pinned to default, got %q", run.Model)
	}
	if err := svc.Consume(context.Background(), run, func(ev engine.Event) error { return nil }); err != nil {
		t.Fatalf("Consume() error: %v", err)
	}
	if err := svc.Persist(run, false, "final"); err != nil {
		t.Fatalf("Persist() error: %v", err)
	}
	run.Release()

	for _, rec := range llm.All() {
		if rec.Model != "test-model" {
			t.Errorf("upstream call used model %q, want pinned %q", rec.Model, "test-model")
		}
	}
}

func TestPrepareFreshPath(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"ok"}, nil).
		StreamResponse([]string{"final"}, nil)
	svc, _ := newService(t, llm)

	req := requestWith([]openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("haz algo")}})
	run, err := svc.Prepare(req)
	if err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}
	defer run.Release()
	if run.ResumedSession != nil {
		t.Errorf("fresh run must not resume a session")
	}
	if run.Lock != nil {
		t.Errorf("fresh run must not hold a session lock")
	}
	if run.SessionID == "" {
		t.Errorf("fresh run must have a new session id")
	}
	if len(run.TurnHistory) != 0 {
		t.Errorf("fresh run must start without turn history")
	}
	if run.Model != "test-model" {
		t.Errorf("expected default model, got %q", run.Model)
	}
}

func TestPrepareNewTurnPath(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse([]string{"ok"}, nil).
		StreamResponse([]string{"final1"}, nil)
	svc, _ := newService(t, llm)

	first := []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("pregunta")}}
	run1, err := svc.Prepare(requestWith(first))
	if err != nil {
		t.Fatalf("Prepare(1) error: %v", err)
	}
	if err := svc.Consume(context.Background(), run1, func(ev engine.Event) error { return nil }); err != nil {
		t.Fatalf("Consume(1) error: %v", err)
	}
	if err := svc.Persist(run1, false, "final1"); err != nil {
		t.Fatalf("Persist(1) error: %v", err)
	}
	run1.Release()

	second := []openai.Message{
		{Role: openai.RoleUser, Content: openai.NewTextContent("pregunta")},
		{Role: openai.RoleAssistant, Content: openai.NewTextContent("final1")},
		{Role: openai.RoleUser, Content: openai.NewTextContent("siguiente")},
	}
	run2, err := svc.Prepare(requestWith(second))
	if err != nil {
		t.Fatalf("Prepare(2) error: %v", err)
	}
	defer run2.Release()
	if run2.ResumedSession != nil {
		t.Errorf("new turn must not be a resume")
	}
	if len(run2.TurnHistory) != 1 || run2.TurnHistory[0] != "final1" {
		t.Errorf("new turn must seed turn_history from the previous session, got %#v", run2.TurnHistory)
	}
	if run2.GoalCtx == nil {
		t.Fatalf("new turn must have a goal context")
	}
	if run2.GoalCtx.PriorContext != "final1" {
		t.Errorf("prior context must come from turn_history, got %q", run2.GoalCtx.PriorContext)
	}
}

func TestPrepareResumePath(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse(nil, []openai.ToolCall{{
			ID: "call_1", Type: "function",
			Function: openai.FunctionCall{Name: "leer", Arguments: "{}"},
		}})
	svc, store := newService(t, llm)

	first := []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("usa la herramienta")}}
	runPausing(t, svc, first)

	llm2 := fakellm.New().
		StreamResponse([]string{"listo"}, nil).
		StreamResponse([]string{"final"}, nil)
	svc2 := service.New(llm2, store, "test-model", 3, 25, noopLogger())

	resume := toolCallMessages("call_1", "contenido leído")
	run2, err := svc2.Prepare(requestWith(resume))
	if err != nil {
		t.Fatalf("Prepare(resume) error: %v", err)
	}
	defer run2.Release()
	if run2.ResumedSession == nil {
		t.Fatalf("resume must mark the resumed session")
	}
	if run2.Lock == nil {
		t.Errorf("resume must hold the session lock")
	}
	if run2.SessionID == "" {
		t.Errorf("resume must reuse the session id")
	}

	var got []string
	if err := svc2.Consume(context.Background(), run2, func(ev engine.Event) error {
		if ev.Kind == engine.EventContent {
			got = append(got, ev.Text)
		}
		return nil
	}); err != nil {
		t.Fatalf("Consume(resume) error: %v", err)
	}
	if len(got) != 1 || got[0] != "final" {
		t.Errorf("expected final content, got %#v", got)
	}
	if llm2.Count() != 2 {
		t.Errorf("resume must not redecompose: expected 2 streams, got %d calls", llm2.Count())
	}
	if !llm2.RecordAt(0).Stream {
		t.Errorf("resume must start with a stream, not a completion")
	}
	if store == nil {
		t.Errorf("store must be wired")
	}
}

func TestResumeLockHeldUntilRelease(t *testing.T) {
	llm := fakellm.New().
		Completion(atomicCompletion()).
		StreamResponse(nil, []openai.ToolCall{{
			ID: "call_1", Type: "function",
			Function: openai.FunctionCall{Name: "leer", Arguments: "{}"},
		}})
	svc, store := newService(t, llm)
	runPausing(t, svc, []openai.Message{{Role: openai.RoleUser, Content: openai.NewTextContent("usa la herramienta")}})

	resume := toolCallMessages("call_1", "contenido")
	block := newBlockingLLM()
	svcB := service.New(block, store, "test-model", 3, 25, noopLogger())

	runACh := make(chan *service.PreparedRun, 1)
	runAErr := make(chan error, 1)
	go func() {
		run, err := svcB.Prepare(requestWith(resume))
		if err != nil {
			runAErr <- err
			return
		}
		runACh <- run
	}()

	select {
	case err := <-runAErr:
		t.Fatalf("Prepare(A) error: %v", err)
	case run := <-runACh:
		runA := run
		consumeDone := make(chan error, 1)
		go func() {
			consumeDone <- svcB.Consume(context.Background(), runA, func(ev engine.Event) error { return nil })
		}()

		// B debe bloquearse en el lock mientras A no libere.
		runBCh := make(chan *service.PreparedRun, 1)
		runBErr := make(chan error, 1)
		go func() {
			run, err := svcB.Prepare(requestWith(resume))
			if err != nil {
				runBErr <- err
				return
			}
			runBCh <- run
		}()
		select {
		case run := <-runBCh:
			t.Fatalf("B must block while A holds the lock, got run %#v", run)
		case err := <-runBErr:
			t.Fatalf("B failed: %v", err)
		case <-time.After(200 * time.Millisecond):
			// bien: B está bloqueado esperando el lock.
		}

		// A termina, persiste y libera.
		block.release()
		select {
		case err := <-consumeDone:
			if err != nil {
				t.Fatalf("Consume(A) error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Consume(A) did not finish after release")
		}
		if err := svcB.Persist(runA, false, "final"); err != nil {
			t.Fatalf("Persist(A) error: %v", err)
		}
		runA.Release()

		// B ya debe proceder (vía nueva, porque A completó el run).
		select {
		case err := <-runBErr:
			t.Fatalf("B failed: %v", err)
		case run := <-runBCh:
			if run.Lock != nil {
				t.Errorf("B should not hold a lock after A completed")
			}
			run.Release()
		case <-time.After(3 * time.Second):
			t.Fatal("B did not proceed after A released the lock")
		}
	}
}

// blockingLLM es un LLM que bloquea el primer stream hasta que se libera,
// para probar la serialización del lock por-sesión.
type blockingLLM struct {
	mu          sync.Mutex
	started     bool
	releaseCh   chan struct{}
	releaseOnce sync.Once
}

func newBlockingLLM() *blockingLLM {
	return &blockingLLM{releaseCh: make(chan struct{})}
}

func (b *blockingLLM) release() {
	b.releaseOnce.Do(func() { close(b.releaseCh) })
}

func (b *blockingLLM) Complete(ctx context.Context, req ports.CompleteRequest) (string, error) {
	return atomicCompletion(), nil
}

func (b *blockingLLM) Stream(ctx context.Context, req ports.StreamRequest, onChunk func(ports.StreamChunk) error) error {
	b.mu.Lock()
	first := !b.started
	if first {
		b.started = true
	}
	b.mu.Unlock()
	if first {
		select {
		case <-b.releaseCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	text := "ok"
	return onChunk(ports.StreamChunk{Delta: openai.Delta{Content: &text}})
}
