package text

// C: src/text/text.h

const (
	FONT_DOMAIN_FALLBACK = 0
	FONT_DOMAIN_DEFAULT  = 1
)

const (
	TR_STYLE_BOLD   = 0x1
	TR_STYLE_ITALIC = 0x2
)

const (
	TR_CODE_START      = 0x7f000001
	TR_CODE_NEWLINE    = 0x7f000002
	TR_CODE_CENTER_ON  = 0x7f000003
	TR_CODE_CENTER_OFF = 0x7f000004
	TR_CODE_BOLD_ON    = 0x7f000005
	TR_CODE_BOLD_OFF   = 0x7f000006
	TR_CODE_ITALIC_ON  = 0x7f000007
	TR_CODE_ITALIC_OFF = 0x7f000008
	TR_CODE_HR         = 0x7f000009
	TR_CODE_FONT_RESET = 0x7f00000a
	TR_CODE_SET_MARGIN = 0x7f000010

	TR_CODE_ALPHA         = 0x7f000100 // Low 8 bit is alpha
	TR_CODE_SHADOW_ALPHA  = 0x7f000200 // Low 8 bit is alpha
	TR_CODE_OUTLINE_ALPHA = 0x7f000300 // Low 8 bit is alpha

	TR_CODE_SIZE_PX    = 0x7f010000 // Low 16 bit is the size in pixels
	TR_CODE_SHADOW     = 0x7f020000 // Low 16 bit is displacement in pixels
	TR_CODE_OUTLINE    = 0x7f030000 // Low 16 bit is thickness in pixels
	TR_CODE_FONT_SIZE  = 0x7f040000 // HTML kinda legacy size
	TR_CODE_SHADOW_US  = 0x7f050000 // Unscaled version
	TR_CODE_OUTLINE_US = 0x7f060000 // Unscaled version

	TR_CODE_COLOR = 0x7e000000 // Low 24 bit is BGR

	TR_CODE_FONT_FAMILY = 0x7d000000 // Low 24 bit is family

	TR_CODE_SHADOW_COLOR  = 0x7c000000 // Low 24 bit is BGR
	TR_CODE_OUTLINE_COLOR = 0x7b000000 // Low 24 bit is BGR
)

const (
	TR_RENDER_DEBUG         = 0x1
	TR_RENDER_ELLIPSIZE     = 0x2
	TR_RENDER_CHARACTER_POS = 0x4
	TR_RENDER_BOLD          = 0x8
	TR_RENDER_ITALIC        = 0x10
	TR_RENDER_SHADOW        = 0x20
	TR_RENDER_OUTLINE       = 0x40
	TR_RENDER_NO_OUTPUT     = 0x80
	TR_RENDER_SUBS          = 0x100 // Render for subtitles
)

const (
	TR_ALIGN_AUTO      = 0
	TR_ALIGN_LEFT      = 1
	TR_ALIGN_CENTER    = 2
	TR_ALIGN_RIGHT     = 3
	TR_ALIGN_JUSTIFIED = 4
)

const (
	TEXT_PARSE_HTML_TAGS     = 0x1
	TEXT_PARSE_HTML_ENTITIES = 0x2
	TEXT_PARSE_SLOPPY_TAGS   = 0x4 // Unknown tags are parsed as normal text
	TEXT_PARSE_SUB_TAGS      = 0x8
	TEXT_PARSE_SLASH_PREFIX  = 0x10
)
