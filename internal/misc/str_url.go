package misc

import (
	"fmt"
	"strconv"
	"strings"
)

const hexchars = "0123456789ABCDEF"

func UrlDeescape(s []byte) {
	d := 0
	i := 0
	for i < len(s) && s[i] != 0 {
		if s[i] == '+' {
			s[d] = ' '
			d++
			i++
		} else if s[i] == '%' {
			i++
			var v byte
			switch {
			case i < len(s) && s[i] >= '0' && s[i] <= '9':
				v = (s[i] - '0') << 4
			case i < len(s) && s[i] >= 'a' && s[i] <= 'f':
				v = (s[i] - 'a' + 10) << 4
			case i < len(s) && s[i] >= 'A' && s[i] <= 'F':
				v = (s[i] - 'A' + 10) << 4
			default:
				s[d] = 0
				return
			}
			i++
			switch {
			case i < len(s) && s[i] >= '0' && s[i] <= '9':
				v |= s[i] - '0'
			case i < len(s) && s[i] >= 'a' && s[i] <= 'f':
				v |= s[i] - 'a' + 10
			case i < len(s) && s[i] >= 'A' && s[i] <= 'F':
				v |= s[i] - 'A' + 10
			default:
				s[d] = 0
				return
			}
			i++
			s[d] = v
			d++
		} else {
			s[d] = s[i]
			d++
			i++
		}
	}
	s[d] = 0
}

var urlEscapeParam = [256]byte{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // 0x00
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // 0x10
	2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 0, // 0x20
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, // 0x30
	0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x40
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 1, // 0x50
	0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x60
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 1, 0, // 0x70
	// 0x80-0xff all zero
}

var urlEscapePath = [256]byte{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // 0x00
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // 0x10
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, // 0x20
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, // 0x30
	0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x40
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 1, // 0x50
	0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x60
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 1, 0, // 0x70
	// 0x80-0xff all zero
}

var urlEscapeSpaceOnly = [256]byte{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // 0x00
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // 0x10
	0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x20
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x30
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x40
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x50
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x60
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x70
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x80
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0x90
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0xa0
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0xb0
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0xc0
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0xd0
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0xe0
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 0xf0
}

func UrlEscape(dst []byte, size int, src []byte, how int) int {
	var s byte
	r := 0
	var table *[256]byte

	switch how {
	case URLEscapePath:
		table = &urlEscapePath
	case URLEscapeParam:
		table = &urlEscapeParam
	case URLEscapeSpaceOnly:
		table = &urlEscapeSpaceOnly
	default:
		panic("url_escape: invalid how")
	}

	i := 0
	for {
		if i >= len(src) {
			break
		}
		s = src[i]
		i++
		if s == 0 {
			break
		}
		switch table[s] {
		case 0:
			if r < size-3 {
				dst[r] = '%'
				dst[r+1] = hexchars[(s>>4)&0xf]
				dst[r+2] = hexchars[s&0xf]
			}
			r += 3

		case 2:
			s = '+'
			fallthrough
		case 1:
			if r < size-1 {
				dst[r] = s
			}
			r++
		}
	}
	if r < size {
		dst[r] = 0
	}
	return r + 1
}

func UrlSplit(proto []byte, protoSize int,
	authorization []byte, authorizationSize int,
	hostname []byte, hostnameSize int,
	portPtr *int,
	path []byte, pathSize int,
	url string) {

	// av_url_split implementation
	var ls, at int

	p := 0
	// get protocol
	ls = strings.Index(url[p:], ":/")
	if ls < 0 {
		ls = strings.IndexAny(url[p:], "/?#")
		if ls < 0 {
			ls = len(url)
		} else {
			ls += p
		}
	} else {
		ls += p
	}
	if proto != nil {
		n := min(ls-p, protoSize-1)
		copy(proto, url[p:p+n])
		if n < len(proto) {
			proto[n] = 0
		}
	}

	// if protocol is empty, the whole URL is a path
	if ls == p || (ls < len(url) && url[ls] == '/' && (ls+1 >= len(url) || url[ls+1] != '/')) {
		// no protocol: ls points at '/' or end
		if ls < len(url) && url[ls] == ':' {
			// "proto:" without //
		}
	}

	// skip "://" or ':' — match av_url_split:
	// p = ls; if (*p == '/') p++; if (*p == '/') p++; // wait, exact code:
	p = ls
	if p < len(url) && url[p] == ':' {
		p++
		if p < len(url) && url[p] == '/' {
			p++
			if p < len(url) && url[p] == '/' {
				p++
			}
		}
	}

	// separate authorization from hostname
	// find end of hostname part (first '/' or end)
	ls = len(url)
	for k := p; k < len(url); k++ {
		if url[k] == '/' {
			ls = k
			break
		}
	}
	// find '@' and ':' within [p, ls)
	at = -1
	col := -1
	for k := p; k < ls; k++ {
		if url[k] == '@' {
			at = k
		} else if url[k] == ':' && at == -1 {
			col = k
		}
	}

	if at >= 0 {
		if authorization != nil {
			n := min(at-p, authorizationSize-1)
			copy(authorization, url[p:p+n])
			if n < len(authorization) {
				authorization[n] = 0
			}
		}
		p = at + 1
		// re-scan for ':' after @
		col = -1
		for k := p; k < ls; k++ {
			if url[k] == ':' {
				col = k
				break
			}
		}
	}

	hostEnd := ls
	if col >= 0 {
		hostEnd = col
		if portPtr != nil {
			port, _ := strconv.Atoi(url[col+1 : ls])
			*portPtr = port
		}
	} else if portPtr != nil {
		*portPtr = -1
	}

	if hostname != nil {
		n := min(hostEnd-p, hostnameSize-1)
		copy(hostname, url[p:p+n])
		if n < len(hostname) {
			hostname[n] = 0
		}
	}

	if path != nil {
		n := min(len(url)-ls, pathSize-1)
		copy(path, url[ls:ls+n])
		if n < len(path) {
			path[n] = 0
		}
	}
}

func UrlResolveRelative(proto, hostname string, port int, path, ref string) string {
	s := 0

	// Check if ref starts with a valid scheme
	for {
		if s >= len(ref) {
			break
		}
		c := ref[s]
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '+' || c == '.' || c == '-' {
			s++
			continue
		}
		break
	}

	if strings.HasPrefix(ref[s:], "://") {
		return ref
	}

	pathlen := 0

	if len(ref) == 0 || ref[0] != '/' {
		if x := strings.LastIndexByte(path, '?'); x >= 0 {
			path = path[:x]
		}

		if r := strings.LastIndexByte(path, '/'); r >= 0 {
			pathlen = r + 1
		}
	}

	if port != -1 {
		return fmt.Sprintf("%s://%s:%d%.*s%s", proto, hostname, port,
			pathlen, path, ref)
	}

	return fmt.Sprintf("%s://%s%.*s%s", proto, hostname,
		pathlen, path, ref)
}

func UrlResolveRelativeFromBase(base, url string) string {
	proto := make([]byte, 20)
	hostname := make([]byte, 200)
	path := make([]byte, 4096) // matches fileaccess urlMax (raised from 2048)
	var port int

	UrlSplit(proto, len(proto), nil, 0, hostname, len(hostname),
		&port, path, len(path), base)

	return UrlResolveRelative(CStr(proto), CStr(hostname),
		port, CStr(path), url)
}
