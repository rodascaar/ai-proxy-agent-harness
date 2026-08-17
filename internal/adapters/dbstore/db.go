// Package dbstore persiste sesiones y conversaciones en una base SQLite única
// (modernc.org/sqlite, puro Go, sin CGO). Implementa los puertos
// session.Store y conversation.Store del dominio.
//
// Configuración de la base (ver https://sqlite.org/pragma.html y la guía de
// mejores prácticas de SQLite en Go):
//
//   - journal_mode=WAL: lecturas concurrentes mientras una escritura avanza.
//     WAL es persistente (se fija una vez); es el modo recomendado para un
//     servidor con más lecturas que escrituras.
//   - foreign_keys=ON: SQLite no la habilita por defecto y es POR CONEXIÓN;
//     por eso va en el DSN, no en db.Exec. Activa el ON DELETE CASCADE de
//     conversations -> messages.
//   - busy_timeout=5000: ante contención, espera hasta 5s en vez de fallar al
//     instante con SQLITE_BUSY.
//   - synchronous=NORMAL: con WAL es seguro (transacciones atómicas y
//     consistentes ante crash de la app; solo se pierde durabilidad ante
//     corte de energía, irrelevante aquí).
//   - _txlock=immediate: BEGIN IMMEDIATE adquiere el lock de escritura al
//     abrir la transacción y evita el SQLITE_BUSY por upgrade de una
//     transacción diferida.
//
// SQLite es single-writer: el pool se limita a una conexión (MaxOpenConns=1)
// para serializar todo acceso y eliminar los "database is locked"
// intra-proceso, la solución portable recomendada.
package dbstore

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// timeLayout es el formato de todos los timestamps persistidos (RFC3339 en
// UTC). Al ser texto en la misma zona, las comparaciones de strings ordenan
// correctamente.
const timeLayout = time.RFC3339

// defaultBusyTimeoutMs es el busy_timeout de cada conexión.
const defaultBusyTimeoutMs = 5000

// Store agrupa el pool SQLite y la política de evicción de sesiones.
type Store struct {
	db          *sql.DB
	ttl         time.Duration
	maxSessions int
	logger      *slog.Logger

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
}

// Open abre (o crea) la base SQLite en path, aplica los PRAGMA por conexión
// vía DSN, limita el pool a una conexión, corre las migraciones embebidas e
// importa los datos legados (notas .sessions/*.md y conversations/*.json) si
// la base está vacía. ttl <= 0 desactiva la expiración por TTL.
func Open(path string, ttl time.Duration, maxSessions int, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating db dir: %w", err)
	}

	dsn := buildDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}
	// SQLite es single-writer: una sola conexión serializa lecturas y
	// escrituras y elimina la contención de locks intra-proceso.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pinging sqlite: %w", err)
	}
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("reading journal_mode: %w", err)
	}
	if journalMode != "wal" {
		_ = db.Close()
		return nil, fmt.Errorf("expected journal_mode=wal, got %q", journalMode)
	}

	s := &Store{
		db:          db,
		ttl:         ttl,
		maxSessions: maxSessions,
		logger:      logger,
		locks:       map[string]*sync.Mutex{},
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	if err := s.importLegacy(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("importing legacy data: %w", err)
	}
	logger.Info("sqlite store ready", "path", path, "journal", journalMode, "ttl", ttl, "max_sessions", maxSessions)
	return s, nil
}

// Close libera el pool de conexiones.
func (s *Store) Close() error {
	return s.db.Close()
}

// buildDSN construye la cadena file:... con los PRAGMA por conexión y el modo
// de transacción inmediata. Los PRAGMA van en el DSN (no en db.Exec) porque se
// aplican a CADA conexión que el pool abra, y foreign_keys/busy_timeout no se
// heredan de una ejecución previa. Se usa q.Add (no q.Set) porque Set
// reemplaza el valor anterior: cada _pragma debe ser un parámetro distinto.
//
// OmitHost evita un gotcha de url.URL.String(): con Host vacío y un Path que no
// empieza con "/", emite "file://data/proxy.db" (doble barra), que SQLite
// interpreta como un URI con autoridad "data" y rechaza con "invalid uri
// authority". Con OmitHost el DSN relativo queda "file:data/proxy.db?...", una
// URI relativa válida. ToSlash normaliza los backslashes de Windows, que en un
// URI de SQLite son el carácter de escape.
func buildDSN(path string) string {
	u := url.URL{Scheme: "file", OmitHost: true, Path: filepath.ToSlash(path)}
	q := u.Query()
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", defaultBusyTimeoutMs))
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	return u.String()
}

// nowUTC devuelve el timestamp de almacenamiento (RFC3339 UTC).
func nowUTC() string {
	return time.Now().UTC().Format(timeLayout)
}

// formatTime convierte un time.Time al formato de almacenamiento (UTC).
func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// parseTime interpreta un timestamp de almacenamiento.
func parseTime(raw string) (time.Time, error) {
	return time.Parse(timeLayout, raw)
}
