// Package task modela el árbol de subtareas que produce la fase de
// descomposición: cada nodo es una tarea que puede tener hijos (sub-tareas)
// o ser atómica (resoluble en un solo paso).
package task

import "strings"

// Node es un nodo del árbol de tareas. El campo Result se llena cuando la
// tarea atómica se ejecuta.
type Node struct {
	Description string  `json:"description"`
	Depth       int     `json:"depth"`
	Children    []*Node `json:"children,omitempty"`
	IsAtomic    bool    `json:"is_atomic,omitempty"`
	Result      string  `json:"result,omitempty"`
}

// NewNode construye un nodo con su descripción y profundidad.
func NewNode(description string, depth int) *Node {
	return &Node{Description: description, Depth: depth}
}

// AddChild agrega un hijo al nodo y lo devuelve.
func (n *Node) AddChild(child *Node) *Node {
	n.Children = append(n.Children, child)
	return child
}

// CollectAtomicLeaves devuelve todas las hojas atómicas del árbol, en orden
// de aparición.
func CollectAtomicLeaves(node *Node) []*Node {
	if node.IsAtomic {
		return []*Node{node}
	}
	var leaves []*Node
	for _, child := range node.Children {
		leaves = append(leaves, CollectAtomicLeaves(child)...)
	}
	return leaves
}

// RenderTree devuelve una representación textual indentada del árbol, para
// exponerla como reasoning_content en la Fase 1.
func RenderTree(root *Node) string {
	var builder strings.Builder
	renderChildren(root, 0, &builder)
	return builder.String()
}

func renderChildren(node *Node, depth int, builder *strings.Builder) {
	for _, child := range node.Children {
		marker := ""
		if child.IsAtomic {
			marker = " (atómica)"
		}
		builder.WriteString(strings.Repeat("  ", depth))
		builder.WriteString("- ")
		builder.WriteString(child.Description)
		builder.WriteString(marker)
		builder.WriteString("\n")
		renderChildren(child, depth+1, builder)
	}
}
