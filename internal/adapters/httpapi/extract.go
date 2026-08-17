package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"
)

// maxExtractOutputRunes acota el texto extraído de un archivo para no inflar el
// payload del chat con documentos gigantes.
const maxExtractOutputRunes = 120_000

// imageExts son las extensiones de imagen soportadas (se devuelven como data
// URL para que el modelo multimodal pueda verlas).
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true,
}

// extractFile expone POST /api/extract-file (multipart/form-data, campo
// "file"): devuelve el contenido útil del archivo para adjuntarlo a un mensaje.
//
//   - imágenes → data URL (parte image_url del protocolo multimodal)
//   - pdf → texto extraído por página (vía github.com/ledongthuc/pdf)
//   - docx → texto de word/document.xml (stdlib zip+xml, sin dependencia)
//   - texto plano (.txt/.md/.json/.csv/código…) → el contenido tal cual
//
// La respuesta es estructurada: {kind, name, size, text|dataUrl}. Un archivo
// mayor a MAX_FILE_BYTES se rechaza con 413.
func (s *Server) extractFile(w http.ResponseWriter, r *http.Request) {
	limit := s.cfg.MaxFileBytes
	if limit < 1 {
		limit = 20 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := r.ParseMultipartForm(limit); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", fmt.Sprintf("archivo mayor a %d bytes", limit))
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid multipart body: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "se requiere un archivo en el campo 'file'")
		return
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", "error leyendo el archivo: "+err.Error())
		return
	}
	if int64(len(data)) > limit {
		s.writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", fmt.Sprintf("archivo mayor a %d bytes", limit))
		return
	}

	name := filepath.Base(header.Filename)
	ext := strings.ToLower(filepath.Ext(name))
	kind := fileKind(ext, data)

	switch kind {
	case kindImage:
		mime := mimeForExt(ext)
		s.writeJSON(w, http.StatusOK, map[string]any{
			"kind":    "image",
			"name":    name,
			"size":    len(data),
			"dataUrl": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
		})
	case kindPDF:
		text, err := extractPDF(data)
		if err != nil {
			s.writeError(w, http.StatusUnprocessableEntity, "extract_error", "no se pudo extraer texto del PDF: "+err.Error())
			return
		}
		s.writeExtractedText(w, name, len(data), text)
	case kindDOCX:
		text, err := extractDOCX(data)
		if err != nil {
			s.writeError(w, http.StatusUnprocessableEntity, "extract_error", "no se pudo extraer texto del DOCX: "+err.Error())
			return
		}
		s.writeExtractedText(w, name, len(data), text)
	default:
		s.writeExtractedText(w, name, len(data), string(data))
	}
}

// fileKind detecta el tipo de archivo por extensión y, en los dudosos, por
// contenido (magic bytes de PDF).
func fileKind(ext string, data []byte) string {
	switch {
	case imageExts[ext]:
		return kindImage
	case ext == ".pdf" || (ext == "" && bytes.HasPrefix(data, []byte("%PDF-"))):
		return kindPDF
	case ext == ".docx" || (ext == "" && bytes.HasPrefix(data, []byte("PK\x03\x04"))):
		return kindDOCX
	default:
		return kindText
	}
}

const (
	kindImage = "image"
	kindPDF   = "pdf"
	kindDOCX  = "docx"
	kindText  = "text"
)

// writeExtractedText responde un archivo de texto extraído, podado a un máximo
// de runas para no inflar el mensaje.
func (s *Server) writeExtractedText(w http.ResponseWriter, name string, size int, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "(el archivo no contiene texto extraíble)"
	}
	if len([]rune(text)) > maxExtractOutputRunes {
		text = string([]rune(text)[:maxExtractOutputRunes]) + "\n[… texto truncado …]"
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"kind": "text",
		"name": name,
		"size": size,
		"text": text,
	})
}

// extractPDF extrae el texto de un PDF con github.com/ledongthuc/pdf.
func extractPDF(data []byte) (string, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var pages []string
	for pageNum := 1; pageNum <= reader.NumPage(); pageNum++ {
		text, err := reader.Page(pageNum).GetPlainText(nil)
		if err != nil {
			return "", fmt.Errorf("page %d: %w", pageNum, err)
		}
		if strings.TrimSpace(text) != "" {
			pages = append(pages, text)
		}
	}
	if len(pages) == 0 {
		return "", errors.New("el documento no tiene páginas con texto")
	}
	return strings.Join(pages, "\n\n"), nil
}

// extractDOCX extrae el texto de un DOCX parseando word/document.xml con la
// stdlib (zip + xml), sin dependencias externas.
func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var document *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			document = f
			break
		}
	}
	if document == nil {
		return "", errors.New("word/document.xml no encontrado")
	}
	rc, err := document.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()

	dec := xml.NewDecoder(rc)
	var sb strings.Builder
	inParagraph := false
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parsing document.xml: %w", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "p":
				inParagraph = true
			case "t":
				var text string
				if err := dec.DecodeElement(&text, &element); err != nil {
					return "", fmt.Errorf("decoding run text: %w", err)
				}
				sb.WriteString(text)
			}
		case xml.EndElement:
			if element.Name.Local == "p" && inParagraph {
				sb.WriteString("\n")
				inParagraph = false
			}
		}
	}
	if sb.Len() == 0 {
		return "", errors.New("el documento no contiene texto")
	}
	return strings.TrimSpace(sb.String()), nil
}

// mimeForExt mapea una extensión de imagen a su MIME para la data URL.
func mimeForExt(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return "application/octet-stream"
	}
}
