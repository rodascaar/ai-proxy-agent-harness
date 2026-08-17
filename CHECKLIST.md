# CHECKLIST — Proxy de descomposición atómica

Marco los checks a medida que se completan. Cada ítem marcado `[mejora]`
es una mejora de este proxy sobre el comportamiento base.

## Fase 0 — Fundaciones

- [x] Módulo Go (`go.mod`) y estructura hexagonal (`cmd/`, `internal/`)
- [x] `config`: lectura de entorno + `.env` (godotenv) + validación fail-fast
- [x] `prompts`: los 7 prompts en español embebidos con `go:embed` y render `{placeholder}`
- [x] `core/openai`: DTOs del wire protocol (Message, Content multimodal, Tool, ToolCall, Delta, request/response)
- [x] `core/content`: separar/recomponer contenido multimodal (Split/Build)
- [x] `core/task`: TaskNode, `CollectAtomicLeaves`, `RenderTree`
- [x] Tests unitarios de config, content y task
- [x] `.env.example`, `.gitignore`, `Makefile`, `README.md`, `CHECKLIST.md`

## Fase 1 — Dominio

- [x] `core/goal`: `GoalContext` (caller_system, turn_instruction, prior_context, image_parts)
- [x] `core/goal`: `_flatten_messages` (tool → `[resultado de herramienta]`, tool_calls → `(llamó a herramienta(s))`)
- [x] `core/goal`: `_find_turn_boundary` (último assistant con texto real)
- [x] `core/goal`: fallback defensivo si el turno actual queda vacío
- [x] `core/ports`: interface `LLMClient` (Complete + Stream)
- [x] `core/engine`: evento de 3 fases (planificar/ejecutar/sintetizar) vía callback
- [x] `core/engine`: descomposición recursiva hasta `MAX_DECOMPOSITION_DEPTH`
- [x] `core/engine`: parse del JSON de descomposición con fallback a "atómica" (regex `{...}`)
- [x] `core/engine`: merge de deltas de tool_calls por índice (id/type/name/arguments append)
- [x] `core/engine`: `_run_phase` compartido por hojas y síntesis (streaming + tool_calls + límite de rondas)
- [x] `core/engine`: ejecución de hojas con contexto acumulado (`results`)
- [x] `core/engine`: síntesis final
- [x] `core/engine`: pausa/reanudación unificada (hoja o síntesis) tras tool call
- [x] `core/engine`: `resume` continúa hojas restantes después de reanudar una hoja
- [x] `core/engine`: `_normalize_tool_choice` — tool_choice del caller NO se propaga internamente (siempre `auto`)
- [x] `core/engine`: límite de rondas de tools → quita tools y fuerza texto final
- [x] `core/engine`: `_compose_system` — system del caller se compone (no se reemplaza)
- [x] `core/engine`: `_tools_summary` — Fase 1 ve tools como texto, nunca funcionales
- [x] `core/engine`: placeholder "ninguna herramienta disponible" sin tools
- [x] `core/engine`: imágenes adjuntas a todas las fases (multimodal)
- [x] `core/session`: `hash_chain` estable por prefijo (digest encadenado)
- [x] `core/session`: `is_valid_resume` (hoja y síntesis)
- [x] `core/session`: `is_new_turn` (sesión completada + mensajes nuevos)
- [x] `core/session`: sesión pendiente sin tool outputs → ni resume ni new turn
- [x] `core/session`: `extract_tool_outputs`
- [x] `core/session`: interface `Store` (FindMatching / Save)
- [x] Tests unitarios espejo de `test_engine.py`, `test_session.py`, `test_goal_context.py`

## Fase 2 — Adaptadores de salida

- [x] `adapters/upstream`: `Complete` no-streaming (+`response_format: json_object`)
- [x] `adapters/upstream`: `Stream` SSE parseando deltas y `[DONE]`
- [x] `adapters/upstream`: error tipado `upstream.Error{Status, Body}`
- [x] `adapters/upstream`: `http.Client` compartido (pooling) — `[mejora]`
- [x] `adapters/upstream`: omite `Authorization` si no hay API key — `[mejora]`
- [x] `adapters/sessionstore/md`: formato nota markdown (resumen legible + JSON en fence)
- [x] `adapters/sessionstore/md`: write-through atómico (temp + rename)
- [x] `adapters/sessionstore/md`: carga de notas al iniciar (recupera tras reinicio)
- [x] `adapters/sessionstore/md`: TTL + `max_sessions` con evicción y borrado del `.md`
- [x] Tests con `httptest.Server` (upstream fake que graba payloads) + directorio temporal

## Fase 3 — Aplicación + HTTP + streaming

- [x] `application/service`: `PrepareRun` (resume / new-turn / fresh) sin redecomponer
- [x] `application/service`: `Consume` (Run o Resume según la vía)
- [x] `application/service`: `Persist` (turn_history acumulado, lock heredado)
- [x] `application/service`: lock por-sesión en la vía resume (evita doble ejecución)
- [x] `adapters/httpapi`: schemas de respuesta OpenAI (completion + chunk + error)
- [x] `adapters/httpapi`: `POST /v1/chat/completions` no-streaming
- [x] `adapters/httpapi`: streaming SSE (`role → reasoning? → content → final → [DONE]`)
- [x] `adapters/httpapi`: streaming con tool_calls (`tool_calls → final(tool_calls) → [DONE]`) y persist en pausa
- [x] `adapters/httpapi`: `GET /v1/models` y `GET /healthz`
- [x] `adapters/httpapi`: error upstream → 502 `upstream_error`; request inválido → 400
- [x] `cmd/proxy/main.go`: composition root + graceful shutdown
- [x] E2E: turno 2 no redecompone turno 1
- [x] E2E: turno nuevo sin sesión válida responde igual (fallback correcto)
- [x] E2E: `EXPOSE_REASONING_CONTENT=false` oculta reasoning
- [x] E2E: round-trip de tool call vía HTTP no redecompone
- [x] E2E: streaming SSE completo

## Fase 4 — Endurecimiento

- [x] Middleware de request-id + logging estructurado + recovery
- [x] `go vet` limpio
- [x] `.golangci.yml` + `golangci-lint` limpio
- [x] Tests con `-race` (Makefile)
- [x] README completo (configuración, endpoints, ejemplos con Ollama)
- [x] Pasada final del checklist y resumen de sesión

## Fase 5 — Web UI + detección dinámica de modelos

- [x] `config`: defaults neutrales (upstream requerido, sin marca DeepSeek)
- [x] `config`: `Values()`, `ValidateValues()`/`ValidateOverride()`, `WriteEnvFile()`
- [x] `service`: reenvío del `model` del request con fallback a `UPSTREAM_MODEL` (fresh + resume)
- [x] `ports`: `ModelLister` (opcional) para consultar `GET /v1/models` del upstream
- [x] `upstream`: `ListModels()` + `Probe()` (implementan `ModelLister` / healthcheck)
- [x] `httpapi`: `GET /v1/models` (passthrough + *cache* + modelo por defecto); `Server.New(svc, cfg, lister, logger)`
- [x] `cmd/proxy`: pasa el cliente upstream como `ModelLister` al `httpapi.New`
- [x] `adapters/webui`: UI embebida con `go:embed` (index.html, app.js, style.css vanilla)
- [x] `httpapi`: ruta `/` (Web UI), `GET/PUT /api/config` (API key enmascarada)
- [x] `cmd/proxy`: warmup opcional (`WARMUP_ON_START`)
- [x] Docs: `.env.example`, `README.md` (Web UI, detección dinámica, tabla de config)
- [x] Tests: config, service (reenvío+fallback), upstream (ListModels/Probe), httpapi (config, UI, /v1/models) con `-race`

## Fase 6 — Speculum: debate multi-modelo + self-refine

- [x] `config`: `Upstreams []Upstream` (legado + indexado `UPSTREAM_{1..3}_*`)
- [x] `config`: `DEBATE_ENABLED` (default false) y `DEBATE_ROUNDS` (2-3, default 2) + validación
- [x] `config`: `DefaultModel()` y `HasAPIKey()`; `Values()` expone debate + upstreams
- [x] `ports`: `LLMRouter` (ClientFor / AllModels / Probe + LLMClient + ModelLister)
- [x] `adapters/router`: `Router` enruta por modelo (fallback legado al primer upstream)
- [x] `prompts`: `refine_critic_{system,user}.md` y `refine_refine_{system,user}.md` (español)
- [x] `core/debate`: `Debater.Refine` — crítica→refinamiento con marca `[APROBADA]` de convergencia
- [x] `core/debate`: crítico = modelo distinto del primario si hay ≥2, si no self-refine
- [x] `core/engine`: `Options.Debate` + `debateResult` aplicado a cada hoja atómica
- [x] `core/engine`: el debate falla → conserva el resultado original (mejora, no dependencia)
- [x] `service`: `WithDebate(enabled, rounds, router)` + `engineOptions` compartido fresh/resume
- [x] `cmd/proxy`: construye el `Router` y cablea debate + `httpapi` con `DefaultModel()`
- [x] `webui`: toggle `DEBATE_ENABLED` + input `DEBATE_ROUNDS` (checkbox genérico en `saveConfig`)
- [x] Docs: `.env.example` (multi-upstream + debate), `README.md` (speculum, tabla de config)
- [x] Tests: config (multi-upstream, debate), router (routing, probe, list), debate (self-refine, multi-model, límite de rondas), engine (debate activa/desactiva) con `-race`
- [x] `gofmt` + `go vet` + `golangci-lint` limpios

## Fase 7 — Selector dinámico de modelos en la UI

- [x] `config`: `resolveUpstreams` unifica legado + indexado (legado como primario si no hay `UPSTREAM_1_*`; `UPSTREAM_2/3_*` adicionales)
- [x] `config`: `upstreamFromPrefix` reutilizable; tests de merge legado+adicional e indexed-overrides-legacy
- [x] `httpapi`: `getConfig` expone `defaultModel`; `putConfig` enmascara cualquier `*_API_KEY` vacío
- [x] `webui/index.html`: `<select id="model-select">` + bloques de upstream 1/2/3 (fieldsets) + botón refrescar
- [x] `webui/app.js`: `refreshModels()` consulta `GET /v1/models` y popula el selector; `sendMessage` lee el `<select>`
- [x] `webui/style.css`: estilos para fieldsets y selector de modelo
- [x] Docs: README (selector dinámico + bloques de upstream)
- [x] Tests: config (merge), httpapi (defaultModel en getConfig) con `-race`

## Fase 8 — Detección automática de modelos y fix del CSV legado

- [x] `config`: `UPSTREAM_MODEL` acepta CSV (`splitCSV`) en la ruta legada; test de 2 modelos + `DefaultModel` = primero
- [x] `config`: `ValidateBaseURL` extraído y reutilizado por `validateUpstreams` (DRY)
- [x] `httpapi`: `POST /api/detect-models` — consulta `/v1/models` de un endpoint dado (URL + API key) y devuelve `{reachable, models|error}`; valida URL con `config.ValidateBaseURL`
- [x] `webui`: botón "Detectar" por upstream (rellena `UPSTREAM_N_MODELS` con los modelos reales) + status ✓/✗
- [x] Tests httpapi: detect-models (alcanzable, API key, inalcanzable, URL inválida, body vacío)
- [x] Docs: README (ejemplos 1 servidor con 2 modelos y 2 servidores locales, endpoint detect-models), `.env.example` (CSV + nota Detectar)
- [x] `gofmt` + `go vet` + `golangci-lint` + `go test -race ./...` limpios

## Fase 9 — Calidad de respuestas en modelos locales chicos

- [x] `ports`: `Temperature`, `MaxTokens` y `Stop` en `CompleteRequest`/`StreamRequest`
- [x] `upstream`: `chatPayload` envía sampling (`temperature`/`max_tokens`/`stop`, omitidos si no se piden)
- [x] `config`: `TEMPERATURE` (0-1, default 0.3, fail-fast si inválida) + `Values()`
- [x] `service`: `WithTemperature` + cableado a `engineOptions`
- [x] `engine`: `Options.Temperature/MaxOutputTokens/Logger`; default `max_tokens=4096`
- [x] `engine`: descomposición siempre a temperatura fija `0.2` (JSON estable)
- [x] `engine`: poda del contexto previo y de `resultsContext` a 12k runes (cabeza+cola con marcador) — no satura ventanas chicas
- [x] `engine`: logging de tamaños de prompt por fase (`logPromptSizes`)
- [x] `debate`: `WithSampling` propaga temperatura/`max_tokens` a crítica y refinamiento
- [x] `prompts`: bloque `<disciplina_de_salida>` en ejecución atómica (solo lo que pide la tarea, sin código si no se pide, sin ejemplos extra, ignorar contexto irrelevante)
- [x] `prompts`: `<disciplina_de_consolidacion>` en síntesis (responder solo al objetivo, descartar contenido irrelevante)
- [x] `prompts`: ejemplos de descomposición/ejecución neutralizados (sin anclaje Python/email)
- [x] Tests: upstream (sampling passthrough), config (`TEMPERATURE`), engine (sampling por fase, default max_tokens, poda), debate (`WithSampling`) con `-race`
- [x] Docs: `.env.example` (TEMPERATURE), `README.md` (tabla + nota de enfoque), este checklist
- [x] `gofmt` + `go vet` + `golangci-lint` + `go test -race ./...` limpios

## Fase 10 — Historial de conversaciones + subida de archivos + UI responsive

- [x] `config`: `CONVERSATIONS_DIR` (default `conversations`) y `MAX_FILE_BYTES` (default `20<<20`) + validación
- [x] `adapters/conversationstore`: ledger JSON por conversación (create lazy, título derivado del primer mensaje, `List`/`Get`/`Rename`/`Delete`, write atómico temp+rename, archivos corruptos ignorados)
- [x] `conversationstore`: `ValidateID` (regex `^[a-zA-Z0-9_-]{8,64}$`) — anti path-traversal
- [x] `httpapi`: `GET /api/conversations`, `GET/PATCH/DELETE /api/conversations/{id}` + recording vía header `X-Conversation-ID`
- [x] `httpapi`: recording del turno user al inicio del run (sobrevive a fallos del stream) y del assistant al final si no quedó pausado
- [x] `httpapi`: `POST /api/extract-file` (multipart) — PDF (ledongthuc/pdf), DOCX (stdlib zip+xml), texto plano y código; imágenes como data URL; magic bytes; 413 si excede `MAX_FILE_BYTES`
- [x] `cmd/proxy`: wiring del conversationstore
- [x] `webui`: sidebar de historial (listar, nuevo chat, seleccionar, eliminar, renombrar) con `X-Conversation-ID` en el envío
- [x] `webui`: subida de adjuntos (imagen → data URL client-side; pdf/docx/texto → `/api/extract-file`) con chips removibles
- [x] `webui`: layout responsive (3 columnas ≥1100px con colapsos; drawers <1100px) + botones con `flex:0 0 auto` y `overflow-wrap:anywhere`
- [x] Docs: `.env.example` (`CONVERSATIONS_DIR`, `MAX_FILE_BYTES`), `README.md`, `.gitignore` (`/conversations/`), este checklist
- [x] Tests: store, httpapi (conversations, extract-file, recording) con `-race`
- [x] `gofmt` + `go vet` + `go test -race ./...` limpios
- [x] `golangci-lint` + prueba manual de la UI

## Fase 11 — Robustez contra alucinaciones + estabilidad visual

- [x] `prompts`: placeholder `<tools_disponibles>` en ejecución y síntesis — el modelo sabe SIEMPRE si tiene herramientas reales
- [x] `prompts`: regla explícita — saludos/trivia jamás usan herramientas ni `[[NECESITA_HERRAMIENTA]]`; no simular acciones externas
- [x] `prompts`: ejemplo de marcador neutralizado (config.yaml → `/opt/data/inventario_2026.dat`) para eliminar el anclaje
- [x] `engine`: detección del marcador `[[NECESITA_HERRAMIENTA: …]]` en resultados de hoja (regex)
- [x] `engine`: sin tools del caller → reintento correctivo UNA vez; si persiste → nota honesta de pendiente (nunca fabricar)
- [x] `engine`: corrección aplicada también en la vía `Resume` (hoja)
- [x] `engine`: sanitización del marcador en el contexto de síntesis cuando no hay tools (defensa en profundidad)
- [x] Tests: reintento correctivo, fallback persistente, marcador preservado con tools, helpers + render de prompts con `<tools_disponibles>` con `-race`
- [x] `webui`: burbujas sin desborde (`overflow-wrap:anywhere` + `word-break`, `pre` contenido, imágenes `max-width:100%`, `max-width:min(82%,760px)`)
- [x] `webui`: sidebar flex (título con ellipsis garantizado + botón estable), `overflow-x:hidden`
- [x] `webui`: config sin desbordes (`overflow-x:hidden`, inputs con `text-overflow:ellipsis`, nombres de modelo truncados con `title`)
- [x] `webui`: red de seguridad global (`overflow-x:hidden` en html/body, status del topbar con ellipsis en móvil)
- [x] `gofmt` + `go vet` + `go test -race ./...` + `golangci-lint` limpios

