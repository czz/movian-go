// fa_http.go — canonical port of src/fileaccess/fa_http.c.
//
// Implements the HTTP/HTTPS and WebDAV/WebDAVS file-access protocols and
// the http_req()/http_reqv() tagged request engine: connection pooling with
// keep-alive parking, redirect + permanent-redirect cache, RFC 2109 cookie
// jar (persisted via htsmsg_store through the cookie bridge), Basic-auth cache
// (keyring), request inspectors, chunked/gzip decoding, range reads with
// streaming-mode switchover, and WebDAV PROPFIND.

package fileaccess

import (
	"fmt"
	"strings"

	"github.com/czz/movian-go/internal/misc"
)

// appendBuf — C: append_buf (decoded_data that accumulates into a buf_t).
func appendBuf(hf *httpFile, hra *HTTPReqAux, data []byte) int {
	b, _ := hra.decodedOpaque.(*misc.Buf)
	if b == nil {
		setErrbuf(hra.errbuf, "out of memory")
		return -1
	}
	cur := b.Len()
	nb := misc.BufCreate(cur + len(data))
	copy(nb.C8()[:cur], b.C8()[:cur])
	copy(nb.C8()[cur:cur+len(data)], data)
	nb.SetContentType(b.GetContentType())
	b.Release()
	hra.decodedOpaque = nb
	// C: *ptr = hra->decoded_opaque — ptr is the caller's buf_t** or,
	// for HTTP_BUFFER_INTERNALLY, &hra->result itself.
	if hra.resultPtr != nil {
		*hra.resultPtr = nb
	} else if hra.wantResult {
		hra.result = nb
	}
	return 0
}

// cleanupBuf — C: cleanup_buf.
func cleanupBuf(hra *HTTPReqAux) {
	if b, ok := hra.decodedOpaque.(*misc.Buf); ok && b != nil {
		b.Release()
	}
	hra.decodedOpaque = nil
}

// appendWaste — C: append_waste.
func appendWaste(hf *httpFile, hra *HTTPReqAux, data []byte) int {
	return 0
}

// snprintf128 — C: snprintf into a char[128] buffer (truncate at 127).
func snprintf128(format string, args ...any) string {
	return snprintfN(128, format, args...)
}

// snprintfN — C: snprintf into a char[n] buffer (truncate at n-1).
func snprintfN(n int, format string, args ...any) string {
	s := fmt.Sprintf(format, args...)
	if len(s) > n-1 {
		s = s[:n-1]
	}
	return s
}

// setErrbuf writes s into a C-style errbuf (NUL-terminated, truncated).
func setErrbuf(errbuf []byte, s string) {
	if len(errbuf) == 0 {
		return
	}
	if len(s) > len(errbuf)-1 {
		s = s[:len(errbuf)-1]
	}
	n := copy(errbuf, s)
	errbuf[n] = 0
}

// strtoll0 — C: strtoll(s, NULL, 0): leading whitespace, optional sign,
// "0x" hex prefix, leading "0" octal, else decimal.
func strtoll0(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' ||
		s[i] == '\n' || s[i] == '\v' || s[i] == '\f' || s[i] == '\r') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	base := int64(10)
	if i+2 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') &&
		isHexDigit(s[i+2]) {
		base = 16
		i += 2
	} else if i < len(s) && s[i] == '0' {
		base = 8
	}
	var n int64
	for i < len(s) {
		d := digitVal(s[i])
		if d < 0 || int64(d) >= base {
			break
		}
		n = n*base + int64(d)
		i++
	}
	if neg {
		n = -n
	}
	return n
}

// strtolHex — C: strtol(s, NULL, 16): skips leading whitespace, optional
// sign, optional 0x prefix, then hex digits; stops at first non-hex char
// (chunk extensions like "ff;name=val" parse as 0xff).
func strtolHex(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' ||
		s[i] == '\v' || s[i] == '\f' || s[i] == '\r') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	if i+1 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') {
		i += 2
	}
	var n int64
	for i < len(s) && isHexDigit(s[i]) {
		n = n*16 + int64(digitVal(s[i]))
		i++
	}
	if neg {
		n = -n
	}
	return n
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func digitVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return -1
}

// urlSplitStr — C: url_split → Go strings.
func urlSplitStr(url string) (proto, auth, hostname string, port int,
	path string) {
	// C: url_split(proto[16], auth[128], hostname[HOSTNAME_MAX=256],
	// path[URL_MAX=2048], url)
	var pb [16]byte
	var ab [128]byte
	var hb [256]byte
	var pathb [urlMax]byte
	port = -1
	misc.UrlSplit(pb[:], len(pb), ab[:], len(ab), hb[:], len(hb),
		&port, pathb[:], len(pathb), url)
	return misc.CStr(pb[:]), misc.CStr(ab[:]), misc.CStr(hb[:]), port, misc.CStr(pathb[:])
}

// hexDump — C: hexdump (debug-only content dump).
func hexDump(b []byte) string {
	const hexd = "0123456789abcdef"
	var sb strings.Builder
	for i, c := range b {
		if i%16 == 0 && i > 0 {
			sb.WriteByte('\n')
		} else if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteByte(hexd[c>>4])
		sb.WriteByte(hexd[c&0xf])
	}
	return sb.String()
}
