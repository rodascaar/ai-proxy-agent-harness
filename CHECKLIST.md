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

