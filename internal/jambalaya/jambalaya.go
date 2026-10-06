// Package jambalaya — Gumbo-shaped DOM over golang.org/x/net/html.
//
// Canonical port of the subset of Google's gumbo-parser API that
// src/ecmascript/es_gumbo.c uses: GumboNode/GumboOutput/GumboAttribute,
// node types, tag enum lookup, and attribute access. Parsing is done by
// x/net/html (the same WHATWG HTML5 algorithm Gumbo implements) and the
// resulting tree is converted once into this package's node shape.
package jambalaya

import (
	"strings"

	"golang.org/x/net/html"
)

// C: GumboNodeType (gumbo.h)
type NodeType int

const (
	NodeDocument   NodeType = iota // GUMBO_NODE_DOCUMENT
	NodeElement                    // GUMBO_NODE_ELEMENT
	NodeText                       // GUMBO_NODE_TEXT
	NodeCDATA                      // GUMBO_NODE_CDATA
	NodeWhitespace                 // GUMBO_NODE_WHITESPACE
	NodeComment                    // GUMBO_NODE_COMMENT
	NodeTemplate                   // GUMBO_NODE_TEMPLATE
)

// C: GumboAttribute
type Attribute struct {
	Name  string
	Value string
}

// Node — C: GumboNode. The C union v.{document,element,text} is flattened:
// Tag/Attributes/Children are the element branch, Name the document
// branch, Text the text branch.
type Node struct {
	Type        NodeType
	Tag         Tag         // element.tag (NodeElement/NodeTemplate)
	Text        string      // text.text (text/cdata/whitespace/comment)
	Name        string      // document.name (doctype name)
	Attributes  []Attribute // element.attributes
	Children    []*Node     // element.children
	OriginalTag string      // element.original_tag text (normalized name)
}

// C: GumboOutput
type Output struct {
	Document *Node
	Root     *Node
}

// NormalizedTagname — C: gumbo_normalized_tagname (tag.c:35-38).
func NormalizedTagname(tag Tag) string {
	if tag < 0 || int(tag) >= len(kJambalayaTagNames) {
		return ""
	}
	return kJambalayaTagNames[tag]
}

// tagByName maps lower-cased tag name → Tag (built once).
var tagByName map[string]Tag

func init() {
	tagByName = make(map[string]Tag, len(kJambalayaTagNames))
	for i, n := range kJambalayaTagNames {
		tagByName[n] = Tag(i)
	}
}

// TagEnum — C: gumbo_tag_enum (tag.c:96+). Case-insensitive lookup;
// TagUnknown when not in the table.
func TagEnum(name string) Tag {
	if t, ok := tagByName[strings.ToLower(name)]; ok {
		return t
	}
	return TagUnknown
}

// GetAttribute — C: gumbo_get_attribute (attribute.c).
func GetAttribute(attrs []Attribute, name string) *Attribute {
	for i := range attrs {
		if attrs[i].Name == name {
			return &attrs[i]
		}
	}
	return nil
}

// convert — *html.Node → *Node (single pass; the C side owns no parser
// state so refcount/destroy are no-ops in Go).
func convert(n *html.Node) *Node {
	gn := &Node{}
	switch n.Type {
	case html.DocumentNode:
		gn.Type = NodeDocument
	case html.ElementNode:
		if n.Data == "template" {
			gn.Type = NodeTemplate
		} else {
			gn.Type = NodeElement
		}
		gn.Tag = TagEnum(n.Data)
		gn.OriginalTag = n.Data
		for _, a := range n.Attr {
			gn.Attributes = append(gn.Attributes,
				Attribute{Name: a.Key, Value: a.Val})
		}
	case html.TextNode:
		if strings.TrimSpace(n.Data) == "" {
			gn.Type = NodeWhitespace
		} else {
			gn.Type = NodeText
		}
		gn.Text = n.Data
	case html.CommentNode:
		gn.Type = NodeComment
		gn.Text = n.Data
	case html.DoctypeNode:
		// Gumbo stores doctype on document.name, not as a child.
		return nil
	default:
		return nil
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if cc := convert(c); cc != nil {
			gn.Children = append(gn.Children, cc)
		} else if c.Type == html.DoctypeNode && gn.Type == NodeDocument {
			gn.Name = c.Data
		}
	}
	return gn
}

// ParseWithOptions — C: gumbo_parse_with_options(&kGumboDefaultOptions,
// str, len). kGumboDefaultOptions is the only option set used.
func ParseWithOptions(data string) *Output {
	root, err := html.Parse(strings.NewReader(data))
	out := &Output{}
	if err == nil && root != nil {
		out.Document = convert(root)
		// Gumbo's output.root is the top-level <html> element.
		for _, c := range out.Document.Children {
			if c.Type == NodeElement || c.Type == NodeTemplate {
				out.Root = c
				break
			}
		}
	}
	if out.Document == nil {
		out.Document = &Node{Type: NodeDocument}
	}
	return out
}

// DestroyOutput — C: gumbo_destroy_output. Go: GC'd; kept for parity.
func DestroyOutput(o *Output) {}
