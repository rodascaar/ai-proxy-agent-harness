package content

import (
	"encoding/json"
	"testing"

	"ai-proxy-agent-harness/internal/core/openai"
)

func TestSplitNilContent(t *testing.T) {
	text, parts := Split(nil)
	if text != "" {
		t.Fatalf("expected empty text, got %q", text)
	}
	if len(parts) != 0 {
		t.Fatalf("expected no parts, got %d", len(parts))
	}
}

func TestSplitPlainString(t *testing.T) {
	content := &openai.Content{Text: "hola mundo"}
	text, parts := Split(content)
	if text != "hola mundo" {
		t.Fatalf("expected text, got %q", text)
	}
	if len(parts) != 0 {
		t.Fatalf("expected no parts, got %d", len(parts))
	}
}

func TestSplitMixedParts(t *testing.T) {
	image := openai.NewImagePart("data:image/png;base64,AAA")
	content := &openai.Content{Text: "mira la imagen", Parts: []openai.ContentPart{image}}
	text, parts := Split(content)
	if text != "mira la imagen" {
		t.Fatalf("expected text, got %q", text)
	}
	if len(parts) != 1 || parts[0].Type != openai.PartTypeImage {
		t.Fatalf("expected the image part back, got %#v", parts)
	}
	if parts[0].ImageURL == nil || parts[0].ImageURL.URL != "data:image/png;base64,AAA" {
		t.Fatalf("image url not preserved: %#v", parts[0].ImageURL)
	}
}

func TestBuildTextOnlyReturnsPlainContent(t *testing.T) {
	content := Build("texto plano", nil)
	if !IsTextOnly(content) {
		t.Fatalf("expected text-only content")
	}
	marshaled, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	if string(marshaled) != `"texto plano"` {
		t.Fatalf("expected plain string, got %s", marshaled)
	}
}

func TestBuildWithPartsReturnsArray(t *testing.T) {
	image := openai.NewImagePart("data:image/png;base64,BBB")
	content := Build("texto", []openai.ContentPart{image})
	if IsTextOnly(content) {
		t.Fatalf("expected content with parts")
	}
	if len(content.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(content.Parts))
	}
	marshaled, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	// El texto debe quedar como primera parte (type=text).
	want := `[{"type":"text","text":"texto"},{"type":"image_url","image_url":{"url":"data:image/png;base64,BBB"}}]`
	if string(marshaled) != want {
		t.Fatalf("expected %s, got %s", want, marshaled)
	}
}

func TestBuildWithoutTextKeepsOnlyParts(t *testing.T) {
	image := openai.NewImagePart("data:image/png;base64,CCC")
	content := Build("", []openai.ContentPart{image})
	marshaled, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"image_url","image_url":{"url":"data:image/png;base64,CCC"}}]`
	if string(marshaled) != want {
		t.Fatalf("expected %s, got %s", want, marshaled)
	}
}
