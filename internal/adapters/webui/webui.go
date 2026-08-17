// Package webui sirve la interfaz web embebida del proxy (chat + configuración)
// desde assets estáticos incluidos en el binario con go:embed. Sin dependencias
// externas ni build step: HTML/JS/CSS vanilla.
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var staticFS embed.FS

// asset es un archivo embebido cargado en memoria con su Content-Type y un
// ETag fuerte derivado del contenido.
type asset struct {
	data        []byte
	contentType string
	etag        string
}

// assets mapea la ruta de cada archivo servido con lógica propia (index.html,
// app.js, style.css) a su contenido en memoria. El ETag deriva del contenido,
// así la revalidación no devuelve 304 con datos viejos cuando el archivo
// cambió de build a build (los archivos de go:embed tienen modtime en cero, por
// lo que el ETag de http.FileServer sería solo el tamaño).
var assets = loadAssets()

func loadAssets() map[string]asset {
	m := map[string]asset{}
	types := map[string]string{
		"index.html": "text/html; charset=utf-8",
		"app.js":     "text/javascript; charset=utf-8",
		"style.css":  "text/css; charset=utf-8",
	}
	for name, ct := range types {
		data, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		m[name] = asset{data: data, contentType: ct, etag: `"` + hex.EncodeToString(sum[:16]) + `"`}
	}
	return m
}

// serveAsset responde un asset embebido con ETag fuerte y cache revalidable.
//
// No reemplazar por http.FileServerFS: para archivos de embed.FS el modtime es
// cero, así que el ETag que genera FileServerFS depende solo del tamaño del
// archivo. Dos builds distintos con un asset del mismo tamaño pero contenido
// distinto colisionan en el mismo ETag y el cliente sirve la versión cacheada
// vieja (bug silencioso, difícil de conectar con esta simplificación).
// Además, desde Go 1.23 serveError descarta los headers Cache-Control, ETag,
// Last-Modified y Content-Encoding ya seteados en las respuestas de error de
// ServeContent/ServeFile/FileServerFS (cambio deliberado de net/http, issue
// golang/go#66343; se restaura con GODEBUG=httpservecontentkeepheaders=1).
// serveAsset existe para evitar ambos problemas: sirve desde memoria con un
// ETag derivado del contenido (SHA-256) y sin pasar por la ruta de error.
func serveAsset(w http.ResponseWriter, r *http.Request, a asset) {
	if r.Header.Get("If-None-Match") == a.etag {
		w.Header().Set("ETag", a.etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", a.contentType)
	w.Header().Set("ETag", a.etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(a.data)
}

// Handler devuelve el handler que sirve la UI embebida desde la raíz. La API
// queda en /v1/* y /api/*; el resto de rutas caen aquí (index.html, app.js,
// style.css).
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// No puede fallar: el directorio static existe en tiempo de compilación.
		panic(err)
	}
	files := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if a, ok := assets[name]; ok {
			serveAsset(w, r, a)
			return
		}
		files.ServeHTTP(w, r)
	})
}
