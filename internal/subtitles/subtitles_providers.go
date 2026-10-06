package subtitles

// subtitles_providers.go — canonical counterparts of subtitles.c's
// exported helpers: parse_bgr and hexnibble.

// ParseBgr — C: parse_bgr (subtitles.c:628-647).
// Parses "#RRGGBB" into the native BGR packing used by the renderer.
func ParseBgr(str string) int {
	if len(str) > 0 && str[0] == '#' {
		str = str[1:]
	}

	if len(str) == 6 {
		bgr := 0
		bgr |= hexNibble(str[0]) << 4
		bgr |= hexNibble(str[1])
		bgr |= hexNibble(str[2]) << 12
		bgr |= hexNibble(str[3]) << 8
		bgr |= hexNibble(str[4]) << 20
		bgr |= hexNibble(str[5]) << 16
		return bgr
	}

	return 0
}

// hexNibble — C: hexnibble.
func hexNibble(c byte) int {
	if c >= '0' && c <= '9' {
		return int(c - '0')
	}
	if c >= 'a' && c <= 'f' {
		return int(c - 'a' + 10)
	}
	if c >= 'A' && c <= 'F' {
		return int(c - 'A' + 10)
	}
	return 0
}
