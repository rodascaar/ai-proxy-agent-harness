-- 002_search: full-text search (FTS5) sobre los mensajes. El superpoder de
-- SQLite para historiales de chat: búsqueda instantánea por subcadena/palabra
-- sobre todo el transcript. La columna `data` es el JSON del openai.Message
-- (los términos de texto viven dentro de él), y `rowid` mapea a messages.id
-- para que los triggers de borrado encuentren la fila FTS correspondiente.
--
-- Es una tabla FTS5 CONTENTFUL (sin content=''): la fuente de verdad de cada
-- mensaje está en `messages`, y el índice FTS se mantiene con triggers. Para
-- las tablas contentful la forma de quitar un documento del índice es un
-- DELETE regular por rowid, NO el comando especial 'delete' (ese solo existe
-- para tablas contentless/external-content y aquí fallaría con "SQL logic
-- error").

CREATE VIRTUAL TABLE messages_fts USING fts5(
    conversation_id UNINDEXED,
    data,
    role UNINDEXED
);

CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, conversation_id, data, role)
    VALUES (new.id, new.conversation_id, new.data, new.role);
END;

CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
    DELETE FROM messages_fts WHERE rowid = old.id;
END;