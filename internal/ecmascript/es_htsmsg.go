// Canonical 1:1 port of src/ecmascript/es_htsmsg.c
//
// htsmsg native objects: XML-deserialized messages exposed to JS.
package ecmascript

import (
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/htsmsg"
)

// ES_NATIVE_CLASS(htsmsg, &htsmsg_release) — C: es_htsmsg.c:23
var esNativeHtsmsg = &EcmascriptNativeClass{
	Name:    "htsmsg",
	Release: func(ptr any) { ptr.(*htsmsg.HTSMsg).Release() },
}

func registerEsHtsmsga() {
	ecmascriptRegisterNativeClass(esNativeHtsmsg)
}

// ---------------------------------------------------------------------------
// es_htsmsg_create_from_xml_duk — C: es_htsmsg.c:28-39
// ---------------------------------------------------------------------------

func esHtsmsgCreateFromXmlGaf(ctx *gaftape.Context) int {
	xml := ctx.SafeToString(0)

	m, errstr := htsmsg.DeserializeXML2(xml)
	if m == nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Malformed XML -- %s", errstr)
	}

	esPushNativeObj(ctx, esNativeHtsmsg, m)
	return 1
}

// ---------------------------------------------------------------------------
// es_push_htsmsg_field — C: es_htsmsg.c:44-60
// ---------------------------------------------------------------------------

// EsPushHtsmsgField — C: es_push_htsmsg_field
func EsPushHtsmsgField(ctx *gaftape.Context, f *htsmsg.HTSMsgField) {
	switch f.GetType() {
	case htsmsg.HmfStr:
		ctx.PushString(f.GetStrValue())
	case htsmsg.HmfS64:
		ctx.PushNumber(float64(f.GetS64Value()))
	case htsmsg.HmfDbl:
		ctx.PushNumber(f.GetDblValue())
	default:
		ctx.PushUndefined()
	}
}

// ---------------------------------------------------------------------------
// es_htsmsg_get_value_duk — C: es_htsmsg.c:66-111
// ---------------------------------------------------------------------------

func esHtsmsgGetValueGaf(ctx *gaftape.Context) int {
	m := esGetNativeObj(ctx, 0, esNativeHtsmsg).(*htsmsg.HTSMsg)
	var f *htsmsg.HTSMsgField
	wantAttr := 0

	if ctx.IsNumber(1) {
		i := ctx.RequireInt(1)

		for _, x := range m.GetFields() {
			if x.GetFlags()&htsmsg.HmfXmlAttribute != 0 {
				continue
			}
			if i == 0 {
				f = x
				break
			}
			i--
		}

		if f == nil {
			return 0
		}
	} else {

		str := ctx.SafeToString(1)
		if len(str) > 0 && str[0] == '@' {
			wantAttr = 1
			str = str[1:]
		}

		f = m.FieldFind(str)

		if f == nil {
			return 0
		}

		if wantAttr != 0 {
			if f.GetFlags()&htsmsg.HmfXmlAttribute == 0 {
				return 0
			}
		} else {
			if f.GetFlags()&htsmsg.HmfXmlAttribute != 0 {
				return 0
			}
		}
	}

	resIdx := ctx.PushObject()

	if f.GetChilds() != nil {
		esPushNativeObj(ctx, esNativeHtsmsg, f.GetChilds().Retain())
		ctx.PutPropString(resIdx, "msg")
	}

	EsPushHtsmsgField(ctx, f)
	ctx.PutPropString(resIdx, "value")
	return 1
}

// ---------------------------------------------------------------------------
// es_htsmsg_get_name_duk — C: es_htsmsg.c:116-134
// ---------------------------------------------------------------------------

func esHtsmsgGetNameGaf(ctx *gaftape.Context) int {
	m := esGetNativeObj(ctx, 0, esNativeHtsmsg).(*htsmsg.HTSMsg)
	var f *htsmsg.HTSMsgField
	i := ctx.RequireInt(1)

	for _, x := range m.GetFields() {
		if x.GetFlags()&htsmsg.HmfXmlAttribute != 0 {
			continue
		}
		if i == 0 {
			f = x
			break
		}
		i--
	}

	if f == nil || f.GetName() == "" {
		return 0
	}
	ctx.PushString(f.GetName())
	return 1
}

// ---------------------------------------------------------------------------
// es_htsmsg_enumerate_duk — C: es_htsmsg.c:140-160
// ---------------------------------------------------------------------------

func esHtsmsgEnumerateGaf(ctx *gaftape.Context) int {
	m := esGetNativeObj(ctx, 0, esNativeHtsmsg).(*htsmsg.HTSMsg)
	idx := 0

	ctx.PushArray()

	for _, f := range m.GetFields() {
		if f.GetFlags()&htsmsg.HmfXmlAttribute != 0 {
			continue
		}
		if f.GetName() == "" {
			continue
		}
		ctx.PushString(f.GetName())
		ctx.PutPropIndex(-2, idx)
		idx++
	}
	return 1
}

// ---------------------------------------------------------------------------
// es_htsmsg_length_duk — C: es_htsmsg.c:166-178
// ---------------------------------------------------------------------------

func esHtsmsgLengthGaf(ctx *gaftape.Context) int {
	m := esGetNativeObj(ctx, 0, esNativeHtsmsg).(*htsmsg.HTSMsg)
	var cnt uint32

	for _, f := range m.GetFields() {
		if f.GetFlags()&htsmsg.HmfXmlAttribute != 0 {
			continue
		}
		cnt++
	}
	ctx.PushUint(cnt)
	return 1
}

// ---------------------------------------------------------------------------
// es_htsmsg_print_duk — C: es_htsmsg.c:184-190
// ---------------------------------------------------------------------------

func esHtsmsgPrintGaf(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	m := esGetNativeObj(ctx, 0, esNativeHtsmsg).(*htsmsg.HTSMsg)
	m.Print(ec.ecID)
	return 0
}

// ---------------------------------------------------------------------------
// fnlist + module — C: es_htsmsg.c:195-211
// ---------------------------------------------------------------------------

var fnlistHtsmsg = []gaftape.FunctionListEntry{
	{Key: "createFromXML", Value: esHtsmsgCreateFromXmlGaf, Nargs: 1},
	{Key: "get", Value: esHtsmsgGetValueGaf, Nargs: 2},
	{Key: "enumerate", Value: esHtsmsgEnumerateGaf, Nargs: 1},
	{Key: "length", Value: esHtsmsgLengthGaf, Nargs: 1},
	{Key: "getName", Value: esHtsmsgGetNameGaf, Nargs: 2},
	{Key: "print", Value: esHtsmsgPrintGaf, Nargs: 1},
}

// ES_MODULE("htsmsg", fnlist_htsmsg)
func registerEsHtsmsgb() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "htsmsg",
		Functions: fnlistHtsmsg,
	})
}
