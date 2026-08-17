-- 001_init: esquema base de conversaciones, mensajes y sesiones.
--
-- Diseño: blobs JSON como fuente de verdad + columnas indexadas solo para lo
-- que se consulta/ordena (checkpoint en sesiones, conversation_id en mensajes).
-- Esto mantiene el round-trip exacto de los tipos de dominio (incluido el
-- contenido multimodal con ContentPart.Raw) sin duplicar campos, y hace que las
-- consultas calientes (FindMatching, listado, conteo) sean acceso por índice.

CREATE TABLE conversations (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL DEFAULT '',
    model      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role            TEXT NOT NULL,
    data            TEXT NOT NULL,
    created_at      TEXT NOT NULL
);

CREATE INDEX idx_messages_conversation ON messages(conversation_id, id);

CREATE TABLE sessions (
    session_id      TEXT PRIMARY KEY,
    checkpoint_hash TEXT NOT NULL,
    checkpoint_len  INTEGER NOT NULL,
    state_json      TEXT NOT NULL,
    last_used_at    TEXT NOT NULL
);

CREATE INDEX idx_sessions_checkpoint ON sessions(checkpoint_hash, checkpoint_len);