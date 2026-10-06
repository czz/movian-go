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
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// DAVXMLField — model of an htsmsg field produced by
// htsmsg_xml_deserialize: named element with optional text value and
// a child map (elements + attributes). Exported so the htsmsg bridge
// (pkg/htsmsg/fa_http_bridge.go) can construct it.
type DAVXMLField struct {
	Name   string
	Str    string
	HasStr bool
	IsAttr bool
	Childs *DAVXMLMap
}

// DAVXMLMap — model of an htsmsg map.
type DAVXMLMap struct {
	Fields []*DAVXMLField
}

// bodies. Wired to htsmsg by pkg/htsmsg/fa_http_bridge.go (htsmsg
// parsePropfind — C: parse_propfind.
func parsePropfind(hf *httpFile, xml *DAVXMLMap, fd *Dir) error {

	// Compare deescaped paths — deescape the searched-for path once.
	rpath := []byte(hf.path)
	rpath = append(rpath, 0)
	misc.UrlDeescape(rpath)
	rpathStr := misc.CStr(rpath)

	m := xml.getMap("multistatus")
	if m == nil {
		return errors.New("WEBDAV: DAV:multistatus not found in XML")
	}

	for _, f := range m.Fields {
		if f.Name != "response" {
			continue
		}
		// C: htsmsg_get_map_by_field(f) — NULL unless HMF_MAP
		// (elements bearing text are HMF_STR even with children).
		c := f.Childs
		if c == nil || f.HasStr {
			continue
		}

		// Some DAV servers send an empty href tag for root path "/"
		href, ok := c.getStr("href")
		if !ok || href == "" {
			href = "/"
		}

		// Get rid of http://hostname (lighttpd includes those)
		if q := strings.Index(href, "://"); q >= 0 {
			if s := strings.IndexByte(href[q+3:], '/'); s >= 0 {
				href = href[q+3+s:]
			} else {
				href = "/"
			}
		}

		ehref := append([]byte(href), 0)
		misc.UrlDeescape(ehref)
		ehrefStr := misc.CStr(ehref)

		prop := c.getMapMulti("propstat", "prop")
		if prop == nil {
			continue
		}

		tr := prop.getMap("resourcetype")
		isdir := tr != nil && tr.fieldFind("collection") != nil

		if fd != nil {
			if rpathStr != ehrefStr {
				hc := hf.connection

				var path string
				if !hc.ssl && hc.port == 80 {
					path = fmt.Sprintf("webdav://%s%s", hc.hostname, href)
				} else if hc.ssl && hc.port == 443 {
					path = fmt.Sprintf("webdavs://%s%s", hc.hostname, href)
				} else {
					scheme := "webdav"
					if hc.ssl {
						scheme = "webdavs"
					}
					path = fmt.Sprintf("%s://%s:%d%s",
						scheme, hc.hostname, hc.port, href)
				}

				qi := strings.LastIndexByte(path, '/')
				if qi >= 0 {
					var fname string
					if qi+1 >= len(path) {
						// Trailing slash — keep it in the URL (some
						// webdav servers 301 without it); the filename
						// is the previous path component.
						j := qi - 1
						for j > 0 && path[j-1] != '/' {
							j--
						}
						fname = path[j:qi]
					} else {
						fname = path[qi+1:]
					}
					fb := append([]byte(fname), 0)
					misc.UrlDeescape(fb)
					fname = misc.CStr(fb)

					typ := ContentFile
					if isdir {
						typ = ContentDir
					}
					fde := DirAdd(fd, path, fname, typ)
					if fde != nil {
						fde.StatDone = true
						if !isdir {
							if d, ok := prop.getStr("getcontentlength"); ok {
								fde.Stat.Size = misc.Atoi64(d)
							} else {
								fde.StatDone = false
							}
						}
						if d, ok := prop.getStr("getlastmodified"); ok {
							if t, r := httpnet.HTTPCtime(d); r == 0 {
								fde.Stat.MTime = t
							}
						}
					}
				}
			}
		} else {
			// single entry stat(2)
			fb := append([]byte(href), 0)
			misc.UrlDeescape(fb)
			fname := misc.CStr(fb)

			if rpathStr == fname {
				// This is the path we asked for
				hf.isdir = isdir
				if !isdir {
					if d, ok := prop.getStr("getcontentlength"); ok {
						hf.filesize = misc.Atoi64(d)
					}
				}
				hf.mtime = time.Time{}
				if d, ok := prop.getStr("getlastmodified"); ok {
					if t, r := httpnet.HTTPCtime(d); r == 0 {
						hf.mtime = t
					}
				}
				return nil
			}
		}
	}

	if fd == nil {
		// Server did not include the file we asked for
		return errors.New("WEBDAV: File not found in XML reply")
	}
	return nil
}

// davPropfind — C: dav_propfind.
func davPropfind(hf *httpFile, fd *Dir,
	nonInteractive *int) error {
	fam := hf.fam
	redircount := 0

	for i := 0; i < 5; i++ {
		if hf.connection == nil {
			if err := httpConnect(hf, true, 0); err != nil {
				return err
			}
		}

		var q misc.HtsbufQueue
		q.HtsbufQueueSetup(0)

		var headers, cookies httpnet.HTTPHeaderList
		httpHeadersSetup(&headers, hf)

		depth := 0
		if fd != nil {
			depth = 1
		}
		q.QPrintf("PROPFIND %s HTTP/1.%d\r\nDepth: %d\r\n",
			hf.path, hf.version, depth)

		if err := httpRequestInspect(&headers, &cookies, hf, "PROPFIND",
			nil); err != nil {
			return err
		}

		httpCookieAppend(hf.connection.hostname, hf, &headers, &cookies)
		cookies.Free()

		hf.hfTrace("Webdav sending request for %s (cid=%d)",
			hf.url, hf.connection.id)
		httpHeadersSend(&q, &headers, hf.userReqHeaders)

		if hf.debug {
			traceRequest(&q, hf)
		}

		hf.connection.tc.TCPWriteQueue(&q)
		code := httpReadResponse(hf, hf.userRespHeaders)

		if code == -1 {
			if hf.connection.reused {
				i--
			}
			httpDetach(hf, false, "Read error")
			continue
		}

		switch code {
		case 207: // Multi-part
			buf := httpReadContent(hf)
			if buf == nil {
				return errors.New("Connection lost")
			}
			var xml *DAVXMLMap
			if fam.httpConns.davXML != nil {
				doc, derr := fam.httpConns.davXML(
					buf.C8()[:buf.Len()])
				buf.Release()
				if derr != nil {
					return fmt.Errorf("WEBDAV/PROPFIND: XML parsing failed:\n%s",
						derr.Error())
				}
				xml = doc
			} else {
				buf.Release()
				return errors.New("WEBDAV/PROPFIND: XML parsing unavailable")
			}
			return parsePropfind(hf, xml, fd)

		case 301, 302, 303, 307:
			if err := httpRedirectF(hf, &redircount, code, true); err != nil {
				return err
			}
			continue

		case 401:
			if err := httpAuthenticate(hf, nonInteractive, true); err != nil {
				return err
			}
			continue

		case 405, 501:
			return errors.New("Not a WEBDAV share")

		default:
			httpDrainContent(hf)
			return fmt.Errorf("Unhandled HTTP response %d", code)
		}
	}
	return errors.New("All attempts failed")
}

// davStat — C: dav_stat.
func davStat(fap *FAProtocol, url string, flags int) (*FileStat, error) {
	fam := fap.fam
	hf := &httpFile{fam: fam}
	statcode := -1
	hf.debug = fam.Gconf().EnableHTTPDebug.Load()
	hf.id = int(atomic.AddInt32(&fam.httpConns.fileTally, 1))
	hf.version = 1
	hf.url = url

	var ni *int
	if flags&FaNonInteractive != 0 {
		ni = &statcode
	}
	if err := davPropfind(hf, nil, ni); err != nil {
		httpDestroy(hf)
		return nil, fapError{code: statcode, msg: err.Error()}
	}

	st := &FileStat{} // C: memset(fs, 0, sizeof(struct fa_stat))
	st.Type = ContentFile
	if hf.isdir {
		st.Type = ContentDir
	}
	st.Size = hf.filesize
	st.MTime = hf.mtime

	httpDestroy(hf)
	return st, nil
}

// davScandir — C: dav_scandir.
func davScandir(fap *FAProtocol, fd *Dir, url string,
	flags int) error {
	fam := fap.fam
	hf := &httpFile{fam: fam}
	hf.id = int(atomic.AddInt32(&fam.httpConns.fileTally, 1))
	hf.version = 1
	hf.url = url
	err := davPropfind(hf, fd, nil)
	httpDestroy(hf)
	return err
}

// WebDAVProtocol — fam-level Protocol for webdav:// and webdavs://
// (C: fa_protocol_webdav{,s} — fap_scan = dav_scandir, fap_open =
// http_open).
type WebDAVProtocol struct {
	fam *FileAccessManager // C: implicit global fa context
	ssl bool
}
