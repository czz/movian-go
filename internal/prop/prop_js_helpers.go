package prop

import (
	"fmt"
	"os"
	"strings"
)

// PrintPropTree prints the property tree to stderr (for debugging).
// This is a recursive implementation matching C's prop_print_tree.
func PrintPropTree(p *Prop) {
	if p == nil {
		return
	}
	printPropTreeRecursive(p, 0)
}

func printPropTreeRecursive(p *Prop, indent int) {
	if p == nil {
		return
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	var prefix strings.Builder
	for range indent {
		prefix.WriteString("  ")
	}

	typeStr := "<void>"
	switch p.propType {
	case PropTypeString:
		if s, ok := p.value.(string); ok {
			typeStr = fmt.Sprintf("%q", s)
		} else {
			typeStr = "<string>"
		}
	case PropTypeInt:
		typeStr = fmt.Sprintf("%v", p.value)
	case PropTypeFloat:
		typeStr = fmt.Sprintf("%v", p.value)
	case PropTypeDir:
		typeStr = "<directory>"
	case PropTypeURI:
		if uv, ok := p.value.(URIValue); ok {
			typeStr = fmt.Sprintf("uri:%s (%s)", uv.URL, uv.Title)
		} else {
			typeStr = "<uri>"
		}
	}

	fmt.Fprintf(os.Stderr, "%s%s: %s\n", prefix.String(), p.name, typeStr)
	for _, c := range p.children {
		printPropTreeRecursive(c, indent+1)
	}
}
