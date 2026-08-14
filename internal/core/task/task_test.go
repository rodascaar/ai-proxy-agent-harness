package task

import (
	"strings"
	"testing"
)

func TestCollectAtomicLeavesFlat(t *testing.T) {
	root := NewNode("objetivo", 0)
	leaf1 := root.AddChild(NewNode("paso 1", 1))
	leaf1.IsAtomic = true
	leaf2 := root.AddChild(NewNode("paso 2", 1))
	leaf2.IsAtomic = true

	leaves := CollectAtomicLeaves(root)
	if len(leaves) != 2 {
		t.Fatalf("expected 2 leaves, got %d", len(leaves))
	}
	if leaves[0].Description != "paso 1" || leaves[1].Description != "paso 2" {
		t.Fatalf("unexpected leaf order: %q %q", leaves[0].Description, leaves[1].Description)
	}
}

func TestCollectAtomicLeavesNested(t *testing.T) {
	root := NewNode("objetivo", 0)
	branch := root.AddChild(NewNode("paso complejo", 1))
	inner := branch.AddChild(NewNode("sub-paso 1", 2))
	inner.IsAtomic = true
	branch.AddChild(NewNode("sub-paso 2", 2)).IsAtomic = true

	leaves := CollectAtomicLeaves(root)
	if len(leaves) != 2 {
		t.Fatalf("expected 2 leaves, got %d", len(leaves))
	}
	if leaves[0].Description != "sub-paso 1" || leaves[1].Description != "sub-paso 2" {
		t.Fatalf("unexpected leaf order")
	}
}

func TestCollectAtomicLeavesRootItselfAtomic(t *testing.T) {
	root := NewNode("objetivo simple", 0)
	root.IsAtomic = true
	root.AddChild(NewNode("hijo ignorado", 1))

	leaves := CollectAtomicLeaves(root)
	if len(leaves) != 1 || leaves[0].Description != "objetivo simple" {
		t.Fatalf("expected root itself as the only leaf")
	}
}

func TestRenderTreeNested(t *testing.T) {
	root := NewNode("objetivo", 0)
	branch := root.AddChild(NewNode("paso complejo", 1))
	branch.AddChild(NewNode("sub-paso 1", 2)).IsAtomic = true
	root.AddChild(NewNode("paso simple", 1)).IsAtomic = true

	rendered := RenderTree(root)
	want := "- paso complejo\n  - sub-paso 1 (atómica)\n- paso simple (atómica)\n"
	if rendered != want {
		t.Fatalf("expected:\n%s\ngot:\n%s", want, rendered)
	}
}

func TestRenderTreeEmpty(t *testing.T) {
	root := NewNode("objetivo", 0)
	if rendered := RenderTree(root); strings.TrimSpace(rendered) != "" {
		t.Fatalf("expected empty render for leaf root, got %q", rendered)
	}
}

func TestRenderTreeStripsTrailingWhitespaceFromAtomicLeafRoot(t *testing.T) {
	// El objetivo es que el render nunca se use para la raíz atómica; pero si
	// ocurre, no debe producir salida con ruido.
	root := NewNode("x", 0)
	root.IsAtomic = true
	if rendered := RenderTree(root); rendered != "" {
		t.Fatalf("expected empty render, got %q", rendered)
	}
}
