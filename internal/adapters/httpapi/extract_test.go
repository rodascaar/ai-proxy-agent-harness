package httpapi_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-proxy-agent-harness/internal/testutil/fakellm"
)

// uploadFile dispara POST /api/extract-file con un archivo multipart.
func uploadFile(t *testing.T, handler http.Handler, name string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("creating form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("writing form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/extract-file", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestExtractTextFile(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := uploadFile(t, handler, "notas.md", []byte("# Título\n\ncontenido de ejemplo"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload := decodeResponse[struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		Text string `json:"text"`
	}](t, rec)
	if payload.Kind != "text" || payload.Name != "notas.md" {
		t.Errorf("unexpected payload %#v", payload)
	}
	if !strings.Contains(payload.Text, "contenido de ejemplo") {
		t.Errorf("expected file content in text, got %q", payload.Text)
	}
}

func TestExtractDOCX(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	doc, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("creating docx entry: %v", err)
	}
	xml := `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Hola</w:t></w:r><w:r><w:t> mundo</w:t></w:r></w:p>
    <w:p><w:r><w:t>segundo párrafo</w:t></w:r></w:p>
  </w:body>
</w:document>`
	if _, err := doc.Write([]byte(xml)); err != nil {
		t.Fatalf("writing docx: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}

	rec := uploadFile(t, handler, "notas.docx", buf.Bytes())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload := decodeResponse[struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}](t, rec)
	if payload.Kind != "text" {
		t.Errorf("expected kind text, got %q", payload.Kind)
	}
	if !strings.Contains(payload.Text, "Hola mundo") {
		t.Errorf("expected extracted docx text, got %q", payload.Text)
	}
	if !strings.Contains(payload.Text, "segundo párrafo") {
		t.Errorf("expected second paragraph, got %q", payload.Text)
	}
}

func TestExtractPDF(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := uploadFile(t, handler, "doc.pdf", minimalPDF("Hola desde PDF"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload := decodeResponse[struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}](t, rec)
	if payload.Kind != "text" {
		t.Errorf("expected kind text, got %q", payload.Kind)
	}
	if !strings.Contains(payload.Text, "Hola desde PDF") {
		t.Errorf("expected extracted pdf text, got %q", payload.Text)
	}
}

func TestExtractImageReturnsDataURL(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	// PNG mágico (cabecera de 8 bytes) — el endpoint no lo decodifica, solo
	// lo devuelve como data URL.
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{0x00}, 8)...)
	rec := uploadFile(t, handler, "foto.png", png)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	payload := decodeResponse[struct {
		Kind    string `json:"kind"`
		DataURL string `json:"dataUrl"`
	}](t, rec)
	if payload.Kind != "image" {
		t.Errorf("expected kind image, got %q", payload.Kind)
	}
	if !strings.HasPrefix(payload.DataURL, "data:image/png;base64,") {
		t.Errorf("expected png data url, got %q", payload.DataURL)
	}
}

func TestExtractFileMissing(t *testing.T) {
	handler, _ := newTestServer(t, fakellm.New(), true)
	rec := doJSON(t, handler, http.MethodPost, "/api/extract-file", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 without file, got %d", rec.Code)
	}
}

// minimalPDF genera un PDF de una página con texto, con offsets y xref
// válidos, suficiente para la extracción de texto de la librería.
func minimalPDF(text string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	var offsets []int
	writeObj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	writeObj("<< /Type /Catalog /Pages 2 0 R >>")
	writeObj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	content := fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET", text)
	writeObj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	writeObj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	xrefStart := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(offsets)+1)
	b.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xrefStart)
	return b.Bytes()
}
