# AI Proxy Agent Harness

Proxy HTTP compatible con la API de OpenAI (`/v1/chat/completions`) que se
coloca delante de un LLM upstream (OpenAI-compatible: Ollama, LM Studio,
llama.cpp, vLLM o cualquier API remota OpenAI-compatible) y **descompone cada
instrucción en un árbol de subtareas atómicas**, las resuelve una por una y
luego sintetiza una respuesta final.

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
  (Ollama, LM Studio, llama.cpp, vLLM o cualquier API remota OpenAI-compatible)

## Instalación y ejecución

```bash
cp .env.example .env        # completa tus valores
make run                    # o: go run ./cmd/proxy
```

El proxy queda disponible en `http://127.0.0.1:8000`, exponiendo:

- `POST /v1/chat/completions` — endpoint principal (compatible OpenAI)
- `GET /v1/models` — lista el modelo configurado
- `GET /healthz` — healthcheck
- `/` — Web UI embebida (chat + configuración)
- `GET/PUT /api/config` — ver y editar la configuración (se guarda en `.env`)

Apunta cualquier cliente compatible (SDK oficial, agentes de código, etc.) a
esta URL como `base_url`. Ejemplo con el SDK de OpenAI apuntando a Ollama:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8000/v1", api_key="sk-local")
```

## Interfaz web

Abre `http://127.0.0.1:8000/` para probar el proxy sin depender de un cliente
externo: un chat (con streaming y el `reasoning_content` colapsable) y un panel
de configuración. La UI está **embebida en el propio binario** (HTML/JS/CSS
vanilla vía `go:embed`): no agrega dependencias ni proceso adicional, y ocupa
pocos KB.

El panel de configuración lee/escribe `.env`; los cambios **aplican al
reiniciar el proxy**.

## Modelo del upstream (detección dinámica)

El proxy **reenvía el `model` del request** a todas las llamadas internas al
upstream, con *fallback* a `UPSTREAM_MODEL`. Así el cliente puede elegir entre
los modelos que el upstream expone (por ejemplo `qwen2.5:7b`, `mistral`, etc.)
y el servidor reutiliza el nombre en cada fase del run.

`GET /v1/models` consulta la lista del upstream vía `GET /v1/models` (con
*caching* en memoria) y la mezcla con el modelo por defecto, por lo que la UI
puede desplegar exactamente los modelos disponibles en ese momento. Si el
upstream no responde, el proxy responde con el modelo por defecto para que la UI
nunca quede vacía.

Esto evita disparar recargas de modelo: el cliente elige uno que ya está
servido y el proxy lo reutiliza de forma consistente durante todo el run.

## Debate multi-modelo ("speculum")

Opcionalmente (y desactivado por defecto), el proxy somete el resultado de cada
tarea atómica a un bucle de **crítica y refinamiento** antes de sintetizar:

- Con **un solo modelo** disponible, ese mismo modelo juega ambos roles
  (crítico y refinador): es el patrón *Self-Refine*.
- Con **2-3 modelos** (locales o remotos vía varios upstreams), el crítico es
  un modelo distinto del refinador: los modelos se vigilan entre sí
  (*multi-agent debate*).

El razonamiento del debate (rondas, crítico, refinador) se expone como
`reasoning_content`, para que puedas ver cómo debatieron. El debate se activa
con `DEBATE_ENABLED=true` y se controla con `DEBATE_ROUNDS` (2-3). Si una ronda
del debate falla, el proxy conserva el resultado original: el debate es una
mejora, no una dependencia crítica.

### Múltiples upstreams

Además del upstream legado (`UPSTREAM_BASE_URL`/`UPSTREAM_MODEL`), puedes
configurar hasta 3 upstreams indexados (`UPSTREAM_1_*` … `UPSTREAM_3_*`), cada
uno con su URL, su API key (formato bearer simple) y la lista de modelos que
expone. El proxy enruta cada llamada por nombre de modelo al upstream correcto,
lo que permite combinar Ollama/LM Studio locales con APIs remotas (OpenAI,
Gemini, Claude) en un mismo run y debate.

## Configuración

Todas las variables se leen del entorno (con soporte de `.env`). Se documentan
completas en [`.env.example`](.env.example):

| Variable | Default | Descripción |
| --- | --- | --- |
| `UPSTREAM_BASE_URL` | *(requerido)* | Base del upstream OpenAI-compatible (ej. Ollama: `http://127.0.0.1:11434/v1`, LM Studio: `http://127.0.0.1:1234/v1`, llama.cpp: `http://127.0.0.1:8080/v1`) |
| `UPSTREAM_API_KEY` | `""` | API key del upstream (omitida si vacía) |
| `UPSTREAM_MODEL` | *(requerido)* | Modelo por defecto del upstream (reenviado a menos que el request envíe `model`; también se anuncia en `GET /v1/models`) |
| `UPSTREAM_{1..3}_BASE_URL` | `""` | Upstream indexado (alternativa/complemento al legado); si se define al menos el 1, se usan los indexados |
| `UPSTREAM_{1..3}_MODELS` | `""` | Modelos que expone el upstream indexado (separados por coma) |
| `UPSTREAM_{1..3}_API_KEY` | `""` | API key bearer del upstream indexado |
| `DEBATE_ENABLED` | `false` | Activa el debate (speculum) sobre los resultados atómicos |
| `DEBATE_ROUNDS` | `2` | Rondas de crítica+refinamiento (2-3) |
| `MAX_DECOMPOSITION_DEPTH` | `3` | Profundidad máxima de descomposición |
| `MAX_TOOL_ROUNDS_PER_PHASE` | `25` | Rondas de tools por fase (agotadas → responde texto) |
| `REQUEST_TIMEOUT_SECONDS` | `120s` | Timeout por request al upstream |
| `SESSION_TTL_SECONDS` | `30m` | TTL de las sesiones persistidas |
| `MAX_SESSIONS` | `200` | Límite de sesiones simultáneas |
| `SESSIONS_DIR` | `.sessions` | Directorio de notas de sesión (markdown) |
| `EXPOSE_REASONING_CONTENT` | `true` | Expone el razonamiento como `reasoning_content` |
| `WARMUP_ON_START` | `false` | Verifica el upstream (`/v1/models`) al arrancar |
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

El avance del proyecto se rastrea en [CHECKLIST.md](CHECKLIST.md).