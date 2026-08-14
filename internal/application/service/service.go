// Package service orquesta el dominio: resuelve cada request en una de las
// tres vías (reanudar un run pausado / turno nuevo sobre una conversación ya
// resuelta / run fresco), consume los eventos del motor y persiste el estado
// de la sesión.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"ai-proxy-agent-harness/internal/core/engine"
	"ai-proxy-agent-harness/internal/core/goal"
	"ai-proxy-agent-harness/internal/core/openai"
	"ai-proxy-agent-harness/internal/core/ports"
	"ai-proxy-agent-harness/internal/core/session"
	"ai-proxy-agent-harness/internal/core/task"
)

// Service encapsula el caso de uso del proxy.
type Service struct {
	client                ports.LLMClient
	store                 session.Store
	defaultModel          string
	maxDecompositionDepth int
	maxToolRoundsPerPhase int
	logger                *slog.Logger
}

// New construye el servicio. defaultModel es el modelo a usar cuando el
// request no trae uno; maxDecompositionDepth y maxToolRoundsPerPhase se pasan
// al motor en cada run.
func New(client ports.LLMClient, store session.Store, defaultModel string, maxDecompositionDepth, maxToolRoundsPerPhase int, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		client:                client,
		store:                 store,
		defaultModel:          defaultModel,
		maxDecompositionDepth: maxDecompositionDepth,
		maxToolRoundsPerPhase: maxToolRoundsPerPhase,
		logger:                logger,
	}
}

// PreparedRun es un run ya resuelto y listo para consumirse. Cuando Lock no es
// nil, ya está adquirido por Prepare y el llamador DEBE llamar Release() (vía
// resume); el llamador no debe re-adquirirlo.
type PreparedRun struct {
	Engine         *engine.Engine
	SessionID      string
	GoalCtx        *goal.Context
	Model          string
	Tools          []openai.Tool
	ToolChoice     json.RawMessage
	Messages       []openai.Message
	ResumedSession *session.State
	TurnHistory    []string
	Lock           *sync.Mutex
}

// Release libera el lock por-sesión si Prepare lo adquirió.
func (r *PreparedRun) Release() {
	if r.Lock != nil {
		r.Lock.Unlock()
		r.Lock = nil
	}
}

// Prepare resuelve la request en una de las tres vías:
//
//  1. reanudación de una fase pausada por tool call, sin redecomponer ni
//     reejecutar nada ya resuelto;
//  2. turno nuevo sobre una conversación con sesión viva ya completada, que
//     arranca un run nuevo sembrado con el turn_history acumulado;
//  3. sin sesión utilizable: run fresco.
//
// En la vía resume adquiere el lock por-sesión ANTES de decidir, para evitar
// doble ejecución cuando dos requests idénticas llegan en paralelo.
func (s *Service) Prepare(req *openai.ChatCompletionRequest) (*PreparedRun, error) {
	messages := req.Messages
	requestedModel := req.ResolvedModel(s.defaultModel)

	for {
		state, err := s.store.FindMatching(context.Background(), messages)
		if err != nil {
			return nil, fmt.Errorf("finding session: %w", err)
		}
		if state == nil || !session.IsValidResume(state, messages) {
			return s.prepareFreshOrNewTurn(req, messages, requestedModel, state)
		}

		lock := s.store.Lock(state.SessionID)
		lock.Lock()
		// Re-validación bajo el lock: si otra request ya reanudó y persistió,
		// el estado actual es otro (o ya no es resume) — reintentamos.
		recheck, err := s.store.FindMatching(context.Background(), messages)
		if err != nil {
			lock.Unlock()
			return nil, fmt.Errorf("rechecking session: %w", err)
		}
		if recheck == state && session.IsValidResume(recheck, messages) {
			return s.buildResumeRun(recheck, messages, lock), nil
		}
		lock.Unlock()
	}
}

// Consume ejecuta el run ya preparado, pasando cada evento del motor al
// callback. Para la vía resume usa ExtractToolOutputs para resolver las tool
// calls pendientes.
func (s *Service) Consume(ctx context.Context, run *PreparedRun, onEvent func(engine.Event) error) error {
	if run.ResumedSession != nil {
		outputs := session.ExtractToolOutputs(run.ResumedSession, run.Messages)
		return run.Engine.Resume(ctx, outputs, onEvent)
	}
	return run.Engine.Run(ctx, onEvent)
}

// Persist guarda la sesión tras consumir un run. Si el run quedó pausado por
// tool calls, guarda la fase pendiente para poder reanudarlo; si terminó y
// produjo contenido final, lo acumula en turn_history para sembrar el próximo
// turno externo sin redecomponer el historial.
func (s *Service) Persist(run *PreparedRun, paused bool, finalContent string) error {
	chain, err := session.HashChain(run.Messages)
	if err != nil {
		return fmt.Errorf("hashing messages: %w", err)
	}
	checkpointHash := ""
	if len(chain) > 0 {
		checkpointHash = chain[len(chain)-1]
	}

	turnHistory := append([]string{}, run.TurnHistory...)
	if !paused && finalContent != "" {
		turnHistory = append(turnHistory, finalContent)
	}

	eng := run.Engine
	state := &session.State{
		SessionID:      run.SessionID,
		CheckpointHash: checkpointHash,
		CheckpointLen:  len(run.Messages),
		GoalCtx:        *run.GoalCtx,
		Model:          run.Model,
		Tools:          run.Tools,
		ToolChoice:     run.ToolChoice,
		Root:           eng.Root(),
		Leaves:         append([]*task.Node{}, eng.Leaves()...),
		Results:        append([]string{}, eng.Results()...),
		TurnHistory:    turnHistory,
	}
	if paused {
		state.PendingPhase = eng.PendingPhase()
		state.PendingLeafIndex = eng.PendingLeafIndex()
		state.PendingToolCalls = append([]openai.ToolCall{}, eng.PendingToolCalls()...)
		state.PendingConversation = append([]openai.Message{}, eng.PendingConversation()...)
		state.ToolRoundCount = eng.ToolRoundCount()
	}
	return s.store.Save(context.Background(), state)
}

// buildResumeRun construye el run para reanudar una sesión pausada. lock ya
// está adquirido por Prepare.
func (s *Service) buildResumeRun(state *session.State, messages []openai.Message, lock *sync.Mutex) *PreparedRun {
	goalCtx := state.GoalCtx
	eng := engine.New(s.client, engine.Options{
		Model:                 state.Model,
		Tools:                 state.Tools,
		ToolChoice:            state.ToolChoice,
		MaxDecompositionDepth: s.maxDecompositionDepth,
		MaxToolRoundsPerPhase: s.maxToolRoundsPerPhase,
	})
	eng.SetGoalContext(&goalCtx)
	eng.RestoreTree(state.Root, state.Leaves, state.Results)
	eng.RestorePending(state.PendingPhase, state.PendingLeafIndex, state.PendingToolCalls, state.PendingConversation, state.ToolRoundCount)

	return &PreparedRun{
		Engine:         eng,
		SessionID:      state.SessionID,
		GoalCtx:        &goalCtx,
		Model:          state.Model,
		Tools:          state.Tools,
		ToolChoice:     state.ToolChoice,
		Messages:       messages,
		ResumedSession: state,
		TurnHistory:    state.TurnHistory,
		Lock:           lock,
	}
}

// prepareFreshOrNewTurn construye un run nuevo (fresco o turno nuevo sembrado
// con turn_history) cuando no hay resume válido. state puede ser nil (sin
// sesión) o una sesión ya completada.
func (s *Service) prepareFreshOrNewTurn(req *openai.ChatCompletionRequest, messages []openai.Message, requestedModel string, state *session.State) (*PreparedRun, error) {
	var priorContextOverride *string
	var turnHistory []string
	if state != nil && session.IsNewTurn(state, messages) {
		prior := strings.Join(state.TurnHistory, "\n\n")
		priorContextOverride = &prior
		turnHistory = state.TurnHistory
	}

	goalCtx := goal.Extract(req.Messages, priorContextOverride)
	eng := engine.New(s.client, engine.Options{
		Model:                 requestedModel,
		Tools:                 req.Tools,
		ToolChoice:            req.ToolChoice,
		MaxDecompositionDepth: s.maxDecompositionDepth,
		MaxToolRoundsPerPhase: s.maxToolRoundsPerPhase,
	})
	eng.SetGoalContext(&goalCtx)

	return &PreparedRun{
		Engine:      eng,
		SessionID:   session.NewSessionID(),
		GoalCtx:     &goalCtx,
		Model:       requestedModel,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
		Messages:    messages,
		TurnHistory: turnHistory,
	}, nil
}
