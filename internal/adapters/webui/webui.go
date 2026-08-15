// Package webui sirve la interfaz web embebida del proxy (chat + configuración)
// desde assets estáticos incluidos en el binario con go:embed. Sin dependencias
// externas ni build step: HTML/JS/CSS vanilla.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// Handler devuelve el handler que sirve la UI embebida desde la raíz. La API
// queda en /v1/* y /api/*; el resto de rutas caen aquí (index.html, app.js,
// style.css).
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// No puede fallar: el directorio static existe en tiempo de compilación.
		panic(err)
	}
	return http.FileServerFS(sub)
}
