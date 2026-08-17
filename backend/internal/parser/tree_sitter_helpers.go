//go:build cgo

package parser

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// tsText extracts text from a node
func tsText(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	start := n.StartByte()
	end := n.EndByte()
	if int(start) >= len(src) {
		return ""
	}
	if int(end) > len(src) {
		end = uint32(len(src))
	}
	return string(src[start:end])
}

// tsLine extracts line number (0-indexed)
func tsLine(n *sitter.Node) int {
	if n == nil {
		return 0
	}
	return int(n.StartPoint().Row) + 1
}

// findChildren recursively finds all nodes of a given type
func findChildren(n *sitter.Node, typ string) []*sitter.Node {
	var result []*sitter.Node
	if n == nil {
		return result
	}
	if n.Type() == typ {
		result = append(result, n)
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		result = append(result, findChildren(n.Child(i), typ)...)
	}
	return result
}

// findFirstChild finds the first direct child of a given type
func findFirstChild(n *sitter.Node, typ string) *sitter.Node {
	if n == nil {
		return nil
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c.Type() == typ {
			return c
		}
	}
	return nil
}

// findChildByTypes finds the first direct child matching any of the types
func findChildByTypes(n *sitter.Node, types ...string) *sitter.Node {
	if n == nil {
		return nil
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		for _, t := range types {
			if c.Type() == t {
				return c
			}
		}
	}
	return nil
}
