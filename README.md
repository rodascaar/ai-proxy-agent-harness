# AI Proxy Agent Harness

Proxy HTTP compatible con la API de OpenAI (`/v1/chat/completions`) que se
coloca delante de un LLM upstream (OpenAI-compatible: Ollama, LM Studio,
llama.cpp, vLLM, DeepSeek...) y **descompone cada instrucción en un árbol de
subtareas atómicas**, las resuelve una por una y luego sintetiza una respuesta
final. Es el port a Go, con arquitectura hexagonal, del proyecto
[Nichonauta/atomic_ai](https://github.com/Nichonauta/atomic_ai).

La idea: modelos más pequeños o más baratos suelen fallar en tareas
compuestas. Este proxy fuerza un proceso de tres fases —planificar, ejecutar,
sintetizar— para que cada paso sea lo bastante simple como para resolverse
bien, manteniendo compatibilidad total con clientes que ya hablan el protocolo
de OpenAI (streaming SSE, `tool_calls`, contenido multimodal y
`reasoning_content`).

## Cómo funciona

Cada turno del usuario pasa por tres fases, orquestadas por el motor en
`internal/core/engine`:

1. **Descomposición** — el modelo decide si la instrucción es atómica o si
   conviene dividirla en subtareas, recursivamente hasta una profundidad
   máxima, construyendo un árbol de tareas.
2. **Ejecución de hojas atómicas** — cada tarea atómica se resuelve en su
   propia llamada al modelo con el contexto acumulado de las anteriores. Si el
   modelo pide `tool_calls`, la ejecución se pausa y se devuelven al cliente.
3. **Síntesis final** — con todos los resultados resueltos, se genera la
   respuesta final (el proceso interno se expone como `reasoning_content`).

### Sesiones y pausa/reanudación

Como una tarea atómica o la síntesis pueden requerir `tool_calls`, el proxy
guarda en qué punto del árbol se quedó entre una petición HTTP y la siguiente.
Las sesiones se persisten como **notas markdown** en `SESSIONS_DIR`
(por defecto `.sessions/`), indexadas por un hash encadenado del historial de
mensajes, para:

- reanudar exactamente donde quedó pausado, sin rehacer descomposición ni
  tareas ya resueltas;
- detectar cuándo una request es un turno nuevo sobre una conversación ya
  completada (y sembrarlo con el resumen de turnos previos);
- expirar sesiones por TTL y limitar cuántas se mantienen.

## Arquitectura (hexagonal)

```
cmd/proxy/main.go          → composition root (wiring + graceful shutdown)
internal/application/      → casos de uso (PrepareRun/Consume/Persist)
internal/core/             → dominio puro (engine, goal, session, task, content, openai, ports)
internal/adapters/         → infraestructura (upstream HTTP, sessionstore markdown, httpapi)
internal/config/           → configuración 12-factor
internal/prompts/          → prompts de las fases embebidos (español)
```

El dominio depende solo de interfaces (`core/ports.LLMClient`,
`core/session.Store`); los adaptadores las implementan y son intercambiables.

## Requisitos

- Go 1.24+
- Un endpoint upstream compatible con la API de chat completions de OpenAI
  (Ollama, LM Studio, llama.cpp, vLLM, DeepSeek, etc.)

## Instalación y ejecución

```bash
cp .env.example .env        # completa tus valores
make run                    # o: go run ./cmd/proxy
```

El proxy queda disponible en `http://127.0.0.1:8000`, exponiendo:

- `POST /v1/chat/completions` — endpoint principal (compatible OpenAI)
- `GET /v1/models` — lista el modelo configurado
- `GET /healthz` — healthcheck

Apunta cualquier cliente compatible (SDK oficial, agentes de código, etc.) a
esta URL como `base_url`. Ejemplo con el SDK de OpenAI apuntando a Ollama:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8000/v1", api_key="sk-local")
```

## Configuración

Todas las variables se leen del entorno (con soporte de `.env`). Se documentan
completas en [`.env.example`](.env.example):

| Variable | Default | Descripción |
| --- | --- | --- |
| `UPSTREAM_BASE_URL` | `https://api.deepseek.com` | Base del upstream OpenAI-compatible (ej. Ollama: `http://127.0.0.1:11434/v1`) |
| `UPSTREAM_API_KEY` | `""` | API key del upstream (omitida si vacía). Cae a `DEEPSEEK_API_KEY` |
| `UPSTREAM_MODEL` | `deepseek-v4-flash` | Modelo por defecto |
| `MAX_DECOMPOSITION_DEPTH` | `3` | Profundidad máxima de descomposición |
| `MAX_TOOL_ROUNDS_PER_PHASE` | `25` | Rondas de tools por fase (agotadas → responde texto) |
| `REQUEST_TIMEOUT_SECONDS` | `120s` | Timeout por request al upstream |
| `SESSION_TTL_SECONDS` | `30m` | TTL de las sesiones persistidas |
| `MAX_SESSIONS` | `200` | Límite de sesiones simultáneas |
| `SESSIONS_DIR` | `.sessions` | Directorio de notas de sesión (markdown) |
| `EXPOSE_REASONING_CONTENT` | `true` | Expone el razonamiento como `reasoning_content` |
| `PROXY_HOST` / `PROXY_PORT` | `127.0.0.1` / `8000` | Interfaz y puerto del proxy |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` o `error` |

### Ejemplo con Ollama

```bash
cp .env.example .env
cat >> .env <<'EOF'
UPSTREAM_BASE_URL=http://127.0.0.1:11434/v1
UPSTREAM_MODEL=qwen2.5:7b
EOF
make run
```

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen2.5:7b","stream":false,
       "messages":[{"role":"user","content":"explica la diferencia entre arrays y listas en Python"}]}'
```

```bash
# streaming SSE
curl -N http://127.0.0.1:8000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen2.5:7b","stream":true,
       "messages":[{"role":"user","content":"resume en 5 puntos qué es un proxy"}]}'
```

## Tests

```bash
make test     # go test -race ./...
make vet
make lint     # requiere golangci-lint
```

El avance del port se rastrea en [CHECKLIST.md](CHECKLIST.md).