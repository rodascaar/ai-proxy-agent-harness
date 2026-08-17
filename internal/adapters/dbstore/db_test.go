package dbstore

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuildDSNNoAuthority fija el formato del DSN: nunca debe producir un URI
// con autoridad no vacía, porque SQLite rechaza "file://data/proxy.db" con
// "invalid uri authority" (regresión de Fase 12).
func TestBuildDSNNoAuthority(t *testing.T) {
	paths := []string{"data/proxy.db", filepath.Join(t.TempDir(), "proxy.db")}
	for _, path := range paths {
		dsn := buildDSN(path)
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("buildDSN(%q) = %q: url.Parse error: %v", path, dsn, err)
		}
		if u.Scheme != "file" {
			t.Errorf("buildDSN(%q) scheme = %q, want file", path, u.Scheme)
		}
		if u.Host != "" {
			t.Errorf("buildDSN(%q) = %q: unexpected authority %q (SQLite rejects it)", path, dsn, u.Host)
		}
		if strings.HasPrefix(dsn, "file://data/") {
			t.Errorf("buildDSN(%q) = %q: relative path leaked as URI authority", path, dsn)
		}
	}
}

// TestOpenRelativePath cubre el caso real de DB_PATH=data/proxy.db: una ruta
// relativa debe abrirse y crear el archivo en el directorio de trabajo.
func TestOpenRelativePath(t *testing.T) {
	t.Chdir(t.TempDir())
	store, err := Open("data/proxy.db", time.Hour, 100, nil)
	if err != nil {
		t.Fatalf("Open() with relative path error: %v", err)
	}
	defer func() { _ = store.Close() }()
	if _, err := os.Stat("data/proxy.db"); err != nil {
		t.Errorf("db file not created at relative path: %v", err)
	}
}
