// Code generated from ext/gumbo-parser/src/tag_enum.h + tag_strings.h.
// Canonical tag table for the gumbo DOM adapter (pkg/jambalaya).
package jambalaya

// C: GumboTag (gumbo.h tag_enum)
type Tag int

const (
	TagHtml          Tag = 0
	TagHead          Tag = 1
	TagTitle         Tag = 2
	TagBase          Tag = 3
	TagLink          Tag = 4
	TagMeta          Tag = 5
	TagStyle         Tag = 6
	TagScript        Tag = 7
	TagNoscript      Tag = 8
	TagTemplate      Tag = 9
	TagBody          Tag = 10
	TagArticle       Tag = 11
	TagSection       Tag = 12
	TagNav           Tag = 13
	TagAside         Tag = 14
	TagH1            Tag = 15
	TagH2            Tag = 16
	TagH3            Tag = 17
	TagH4            Tag = 18
	TagH5            Tag = 19
	TagH6            Tag = 20
	TagHgroup        Tag = 21
	TagHeader        Tag = 22
	TagFooter        Tag = 23
	TagAddress       Tag = 24
	TagP             Tag = 25
	TagHr            Tag = 26
	TagPre           Tag = 27
	TagBlockquote    Tag = 28
	TagOl            Tag = 29
	TagUl            Tag = 30
	TagLi            Tag = 31
	TagDl            Tag = 32
	TagDt            Tag = 33
	TagDd            Tag = 34
	TagFigure        Tag = 35
	TagFigcaption    Tag = 36
	TagMain          Tag = 37
	TagDiv           Tag = 38
	TagA             Tag = 39
	TagEm            Tag = 40
	TagStrong        Tag = 41
	TagSmall         Tag = 42
	TagS             Tag = 43
	TagCite          Tag = 44
	TagQ             Tag = 45
	TagDfn           Tag = 46
	TagAbbr          Tag = 47
	TagData          Tag = 48
	TagTime          Tag = 49
	TagCode          Tag = 50
	TagVar           Tag = 51
	TagSamp          Tag = 52
	TagKbd           Tag = 53
	TagSub           Tag = 54
	TagSup           Tag = 55
	TagI             Tag = 56
	TagB             Tag = 57
	TagU             Tag = 58
	TagMark          Tag = 59
	TagRuby          Tag = 60
	TagRt            Tag = 61
	TagRp            Tag = 62
	TagBdi           Tag = 63
	TagBdo           Tag = 64
	TagSpan          Tag = 65
	TagBr            Tag = 66
	TagWbr           Tag = 67
	TagIns           Tag = 68
	TagDel           Tag = 69
	TagImage         Tag = 70
	TagImg           Tag = 71
	TagIframe        Tag = 72
	TagEmbed         Tag = 73
	TagObject        Tag = 74
	TagParam         Tag = 75
	TagVideo         Tag = 76
	TagAudio         Tag = 77
	TagSource        Tag = 78
	TagTrack         Tag = 79
	TagCanvas        Tag = 80
	TagMap           Tag = 81
	TagArea          Tag = 82
	TagMath          Tag = 83
	TagMi            Tag = 84
	TagMo            Tag = 85
	TagMn            Tag = 86
	TagMs            Tag = 87
	TagMtext         Tag = 88
	TagMglyph        Tag = 89
	TagMalignmark    Tag = 90
	TagAnnotationXml Tag = 91
	TagSvg           Tag = 92
	TagForeignobject Tag = 93
	TagDesc          Tag = 94
	TagTable         Tag = 95
	TagCaption       Tag = 96
	TagColgroup      Tag = 97
	TagCol           Tag = 98
	TagTbody         Tag = 99
	TagThead         Tag = 100
	TagTfoot         Tag = 101
	TagTr            Tag = 102
	TagTd            Tag = 103
	TagTh            Tag = 104
	TagForm          Tag = 105
	TagFieldset      Tag = 106
	TagLegend        Tag = 107
	TagLabel         Tag = 108
	TagInput         Tag = 109
	TagButton        Tag = 110
	TagSelect        Tag = 111
	TagDatalist      Tag = 112
	TagOptgroup      Tag = 113
	TagOption        Tag = 114
	TagTextarea      Tag = 115
	TagKeygen        Tag = 116
	TagOutput        Tag = 117
	TagProgress      Tag = 118
	TagMeter         Tag = 119
	TagDetails       Tag = 120
	TagSummary       Tag = 121
	TagMenu          Tag = 122
	TagMenuitem      Tag = 123
	TagApplet        Tag = 124
	TagAcronym       Tag = 125
	TagBgsound       Tag = 126
	TagDir           Tag = 127
	TagFrame         Tag = 128
	TagFrameset      Tag = 129
	TagNoframes      Tag = 130
	TagIsindex       Tag = 131
	TagListing       Tag = 132
	TagXmp           Tag = 133
	TagNextid        Tag = 134
	TagNoembed       Tag = 135
	TagPlaintext     Tag = 136
	TagRb            Tag = 137
	TagStrike        Tag = 138
	TagBasefont      Tag = 139
	TagBig           Tag = 140
	TagBlink         Tag = 141
	TagCenter        Tag = 142
	TagFont          Tag = 143
	TagMarquee       Tag = 144
	TagMulticol      Tag = 145
	TagNobr          Tag = 146
	TagSpacer        Tag = 147
	TagTt            Tag = 148
	TagRtc           Tag = 149
	TagUnknown       Tag = 150
	TagLast          Tag = 151
)

// kJambalayaTagNames — C: tag.c:23 (generated from tag_strings.h)
var kJambalayaTagNames = []string{
	"html",
	"head",
	"title",
	"base",
	"link",
	"meta",
	"style",
	"script",
	"noscript",
	"template",
	"body",
	"article",
	"section",
	"nav",
	"aside",
	"h1",
	"h2",
	"h3",
	"h4",
	"h5",
	"h6",
	"hgroup",
	"header",
	"footer",
	"address",
	"p",
	"hr",
	"pre",
	"blockquote",
	"ol",
	"ul",
	"li",
	"dl",
	"dt",
	"dd",
	"figure",
	"figcaption",
	"main",
	"div",
	"a",
	"em",
	"strong",
	"small",
	"s",
	"cite",
	"q",
	"dfn",
	"abbr",
	"data",
	"time",
	"code",
	"var",
	"samp",
	"kbd",
	"sub",
	"sup",
	"i",
	"b",
	"u",
	"mark",
	"ruby",
	"rt",
	"rp",
	"bdi",
	"bdo",
	"span",
	"br",
	"wbr",
	"ins",
	"del",
	"image",
	"img",
	"iframe",
	"embed",
	"object",
	"param",
	"video",
	"audio",
	"source",
	"track",
	"canvas",
	"map",
	"area",
	"math",
	"mi",
	"mo",
	"mn",
	"ms",
	"mtext",
	"mglyph",
	"malignmark",
	"annotation-xml",
	"svg",
	"foreignobject",
	"desc",
	"table",
	"caption",
	"colgroup",
	"col",
	"tbody",
	"thead",
	"tfoot",
	"tr",
	"td",
	"th",
	"form",
	"fieldset",
	"legend",
	"label",
	"input",
	"button",
	"select",
	"datalist",
	"optgroup",
	"option",
	"textarea",
	"keygen",
	"output",
	"progress",
	"meter",
	"details",
	"summary",
	"menu",
	"menuitem",
	"applet",
	"acronym",
	"bgsound",
	"dir",
	"frame",
	"frameset",
	"noframes",
	"isindex",
	"listing",
	"xmp",
	"nextid",
	"noembed",
	"plaintext",
	"rb",
	"strike",
	"basefont",
	"big",
	"blink",
	"center",
	"font",
	"marquee",
	"multicol",
	"nobr",
	"spacer",
	"tt",
	"rtc",
}
