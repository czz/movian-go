// Canonical 1:1 port of src/ecmascript/es_gumbo.c — HTML parsing module
// backed by pkg/gumbo (Gumbo-shaped DOM over x/net/html).
package ecmascript

import (
	"slices"
	"strings"
	"sync/atomic"

	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/jambalaya"
)

// ---------------------------------------------------------------------------
// es_gumbo_output_t / es_gumbo_node_t — C: es_gumbo.c:25-33
// ---------------------------------------------------------------------------

// C: es_gumbo_output_t
type esJambalayaOutput struct {
	egoRefcount atomic.Int32
	egoOutput   *jambalaya.Output
}

// C: es_gumbo_node_t
type esJambalayaNode struct {
	node   *jambalaya.Node
	output *esJambalayaOutput
}

// esJambalayaOutputRelease — C: es_gumbo_output_release (es_gumbo.c:39-47)
func esJambalayaOutputRelease(ego *esJambalayaOutput) {
	if ego.egoRefcount.Add(-1) != 0 {
		return
	}
	jambalaya.DestroyOutput(ego.egoOutput)
}

// esJambalayaNodeCreate — C: es_gumbo_node_create (es_gumbo.c:53-61)
func esJambalayaNodeCreate(n *jambalaya.Node, ego *esJambalayaOutput) *esJambalayaNode {
	egn := &esJambalayaNode{}
	egn.node = n
	egn.output = ego
	ego.egoRefcount.Add(1)
	return egn
}

// esJambalayaNodeRelease — C: es_gumbo_node_release (es_gumbo.c:67-71)
func esJambalayaNodeRelease(n *esJambalayaNode) {
	esJambalayaOutputRelease(n.output)
}

// C: ES_NATIVE_CLASS(gumbo_node, &es_gumbo_node_release)
var esNativeJambalayaNode = &EcmascriptNativeClass{
	Name:    "gumbo_node",
	Release: func(p any) { esJambalayaNodeRelease(p.(*esJambalayaNode)) },
}

func registerEsJambalayaa() {
	ecmascriptRegisterNativeClass(esNativeJambalayaNode)
}

// pushJambalayaNode — C: push_gumbo_node (es_gumbo.c:77-81)
func pushJambalayaNode(ctx *gaftape.Context, n *jambalaya.Node,
	ego *esJambalayaOutput) {
	esPushNativeObj(ctx, esNativeJambalayaNode, esJambalayaNodeCreate(n, ego))
}

// esJambalayaParse — C: es_gumbo_parse (es_gumbo.c:87-113)
func esJambalayaParse(ctx *gaftape.Context) int {
	str := ctx.GetLstring(0)
	ego := &esJambalayaOutput{}
	ego.egoRefcount.Store(1)
	ego.egoOutput = jambalaya.ParseWithOptions(str)

	ctx.Pop()

	ctx.PushObject()

	pushJambalayaNode(ctx, ego.egoOutput.Document, ego)
	ctx.PutPropString(-2, "document")

	pushJambalayaNode(ctx, ego.egoOutput.Root, ego)
	ctx.PutPropString(-2, "root")

	esJambalayaOutputRelease(ego)
	return 1
}

// esJambalayaNodeType — C: es_gumbo_node_type (es_gumbo.c:119-135)
func esJambalayaNodeType(ctx *gaftape.Context) int {
	v := esGetNativeObj(ctx, 0, esNativeJambalayaNode)
	egn := v.(*esJambalayaNode)
	o := -1
	switch egn.node.Type {
	case jambalaya.NodeDocument:
		o = 9
	case jambalaya.NodeTemplate, jambalaya.NodeElement:
		o = 1
	case jambalaya.NodeWhitespace, jambalaya.NodeCDATA, jambalaya.NodeText:
		o = 3
	case jambalaya.NodeComment:
		o = 8
	}
	ctx.PushInt(o)
	return 1
}

// esJambalayaNodeName — C: es_gumbo_node_name (es_gumbo.c:141-160)
func esJambalayaNodeName(ctx *gaftape.Context) int {
	v := esGetNativeObj(ctx, 0, esNativeJambalayaNode)
	egn := v.(*esJambalayaNode)
	node := egn.node
	switch node.Type {
	case jambalaya.NodeDocument:
		ctx.PushString(node.Name)
	case jambalaya.NodeElement:
		ctx.PushString(jambalaya.NormalizedTagname(node.Tag))
	default:
		ctx.PushString(node.Text)
	}
	return 1
}

// esJambalayaNodeChilds — C: es_gumbo_node_childs (es_gumbo.c:166-194)
func esJambalayaNodeChilds(ctx *gaftape.Context) int {
	v := esGetNativeObj(ctx, 0, esNativeJambalayaNode)
	egn := v.(*esJambalayaNode)
	all := ctx.GetBoolean(1)
	node := egn.node
	ctx.PushArray()

	if node.Type == jambalaya.NodeElement || node.Type == jambalaya.NodeTemplate {
		num := 0
		for _, child := range node.Children {
			if child.Type == jambalaya.NodeWhitespace {
				continue
			}
			if !all {
				if child.Type == jambalaya.NodeText ||
					child.Type == jambalaya.NodeCDATA ||
					child.Type == jambalaya.NodeComment {
					continue
				}
			}
			pushJambalayaNode(ctx, child, egn.output)
			ctx.PutPropIndex(-2, num)
			num++
		}
	}
	return 1
}

// esJambalayaNodeAttributes — C: es_gumbo_node_attributes (es_gumbo.c:200-223)
func esJambalayaNodeAttributes(ctx *gaftape.Context) int {
	v := esGetNativeObj(ctx, 0, esNativeJambalayaNode)
	egn := v.(*esJambalayaNode)
	node := egn.node
	ctx.PushArray()

	if node.Type == jambalaya.NodeElement || node.Type == jambalaya.NodeTemplate {
		for i, attrib := range node.Attributes {
			ctx.PushObject()

			ctx.PushString(attrib.Name)
			ctx.PutPropString(-2, "name")
			ctx.PushString(attrib.Value)
			ctx.PutPropString(-2, "value")
			ctx.PutPropIndex(-2, i)
		}
	}
	return 1
}

// esJambalayaNodeTextContentR — C: es_gumbo_node_textContent_r (es_gumbo.c:226-250)
func esJambalayaNodeTextContentR(ctx *gaftape.Context, node *jambalaya.Node) int {
	sum := 0
	if node.Type == jambalaya.NodeElement || node.Type == jambalaya.NodeTemplate {
		for _, child := range node.Children {
			switch child.Type {
			case jambalaya.NodeText, jambalaya.NodeCDATA, jambalaya.NodeComment:
				ctx.PushString(child.Text)
				sum++
			case jambalaya.NodeElement, jambalaya.NodeTemplate:
				sum += esJambalayaNodeTextContentR(ctx, child)
			}
		}
	}
	return sum
}

// esJambalayaNodeTextContent — C: es_gumbo_node_textContent (es_gumbo.c:255-264)
func esJambalayaNodeTextContent(ctx *gaftape.Context) int {
	v := esGetNativeObj(ctx, 0, esNativeJambalayaNode)
	egn := v.(*esJambalayaNode)
	node := egn.node
	num := esJambalayaNodeTextContentR(ctx, node)
	if num == 0 {
		return 0
	}
	ctx.Concat(num)
	return 1
}

// esJambalayaFindByIdR — C: es_gumbo_find_by_id_r (es_gumbo.c:269-288)
func esJambalayaFindByIdR(node *jambalaya.Node, id string) *jambalaya.Node {
	if node.Type != jambalaya.NodeElement && node.Type != jambalaya.NodeTemplate {
		return nil
	}

	a := jambalaya.GetAttribute(node.Attributes, "id")

	if a != nil && a.Value == id {
		return node
	}

	for _, c := range node.Children {
		if r := esJambalayaFindByIdR(c, id); r != nil {
			return r
		}
	}
	return nil
}

// esJambalayaFindById — C: es_gumbo_find_by_id (es_gumbo.c:294-305)
func esJambalayaFindById(ctx *gaftape.Context) int {
	v := esGetNativeObj(ctx, 0, esNativeJambalayaNode)
	egn := v.(*esJambalayaNode)
	id := ctx.ToString(1)
	r := esJambalayaFindByIdR(egn.node, id)
	if r == nil {
		return 0
	}
	pushJambalayaNode(ctx, r, egn.output)
	return 1
}

// esJambalayaFindByTagNameR — C: es_gumbo_find_by_tag_name_r (es_gumbo.c:311-326)
func esJambalayaFindByTagNameR(node *jambalaya.Node, tag jambalaya.Tag,
	ctx *gaftape.Context, idxp *int, ego *esJambalayaOutput) {
	if node.Type != jambalaya.NodeElement && node.Type != jambalaya.NodeTemplate {
		return
	}

	if node.Tag == tag {
		pushJambalayaNode(ctx, node, ego)
		ctx.PutPropIndex(-2, *idxp)
		*idxp++
	}

	for _, c := range node.Children {
		esJambalayaFindByTagNameR(c, tag, ctx, idxp, ego)
	}
}

// esJambalayaFindByTagName — C: es_gumbo_find_by_tag_name (es_gumbo.c:332-345)
func esJambalayaFindByTagName(ctx *gaftape.Context) int {
	v := esGetNativeObj(ctx, 0, esNativeJambalayaNode)
	egn := v.(*esJambalayaNode)
	tagstr := ctx.ToString(1)
	tag := jambalaya.TagEnum(tagstr)
	if tag == jambalaya.TagUnknown {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Unknown tag %s", tagstr)
	}
	idx := 0
	ctx.PushArray()
	esJambalayaFindByTagNameR(egn.node, tag, ctx, &idx, egn.output)
	return 1
}

// esJambalayaFindByClassR — C: es_gumbo_find_by_class_r (es_gumbo.c:351-380)
func esJambalayaFindByClassR(node *jambalaya.Node, classes []string,
	ctx *gaftape.Context, idxp *int, ego *esJambalayaOutput) {
	if node.Type != jambalaya.NodeElement && node.Type != jambalaya.NodeTemplate {
		return
	}

	a := jambalaya.GetAttribute(node.Attributes, "class")

	if a != nil {
		list := strings.Split(a.Value, " ")
		found := true
		for _, cls := range classes {
			f := slices.Contains(list, cls)
			if !f {
				found = false
				break
			}
		}
		if found {
			pushJambalayaNode(ctx, node, ego)
			ctx.PutPropIndex(-2, *idxp)
			*idxp++
		}
	}

	for _, c := range node.Children {
		esJambalayaFindByClassR(c, classes, ctx, idxp, ego)
	}
}

// esJambalayaFindByClass — C: es_gumbo_find_by_class (es_gumbo.c:386-399)
func esJambalayaFindByClass(ctx *gaftape.Context) int {
	v := esGetNativeObj(ctx, 0, esNativeJambalayaNode)
	egn := v.(*esJambalayaNode)
	cls := ctx.ToString(1)
	idx := 0
	ctx.PushArray()
	classlist := strings.Split(cls, " ")
	esJambalayaFindByClassR(egn.node, classlist, ctx, &idx, egn.output)
	return 1
}

// ---------------------------------------------------------------------------
// fnlist_gumbo — C: es_gumbo.c:403-414
// ---------------------------------------------------------------------------

var esFnlistJambalaya = []gaftape.FunctionListEntry{
	{Key: "parse", Value: esJambalayaParse, Nargs: 1},
	{Key: "nodeType", Value: esJambalayaNodeType, Nargs: 1},
	{Key: "nodeName", Value: esJambalayaNodeName, Nargs: 1},
	{Key: "nodeChilds", Value: esJambalayaNodeChilds, Nargs: 2},
	{Key: "nodeAttributes", Value: esJambalayaNodeAttributes, Nargs: 1},
	{Key: "nodeTextContent", Value: esJambalayaNodeTextContent, Nargs: 1},
	{Key: "findById", Value: esJambalayaFindById, Nargs: 2},
	{Key: "findByTagName", Value: esJambalayaFindByTagName, Nargs: 2},
	{Key: "findByClassName", Value: esJambalayaFindByClass, Nargs: 2},
}

// C: ES_MODULE("gumbo", fnlist_gumbo)
// "gumbo" stays for backwards compatibility with scripts; the module is
// also reachable as "native/jambalaya".
func registerEsJambalayab() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "gumbo",
		Functions: esFnlistJambalaya,
	})
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "jambalaya",
		Functions: esFnlistJambalaya,
	})
}
