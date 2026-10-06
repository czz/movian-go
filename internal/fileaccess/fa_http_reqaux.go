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
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// httpQueryArg — C: http_query_arg_t.
type httpQueryArg struct {
	key    string
	val    string
	valLen int
}

// HTTPBufferInternally — C: HTTP_BUFFER_INTERNALLY ((void *)-1) for
// HTTP_TAG_RESULT_PTR: the result is stored in hra->result and
// retrieved via HTTPReqGetResult (async mode).
var HTTPBufferInternally = &struct{ httpBufferInternally int }{}

// HTTPReqAux — C: http_req_aux_t.
type HTTPReqAux struct {
	refcount       int32
	total          int64
	bytesCompleted int64
	cb             FALoadCB
	opaque         any
	tmpbuf         []byte
	errbuf         []byte

	encodedData func(hf *httpFile, hra *HTTPReqAux, data []byte) int
	decodedData func(hf *httpFile, hra *HTTPReqAux, data []byte) int

	gzip          *hraGzip // C: z_stream zstream
	decodedOpaque any

	decodedCleanup func(hra *HTTPReqAux)

	hf              *httpFile
	method          string
	post            bool
	postdata        misc.HtsbufQueue
	postContentType string
	wantResult      bool
	arguments       []string // C: char **arguments (k/v pairs)
	queryArgs       []httpQueryArg
	flags           int

	headersIn  httpnet.HTTPHeaderList
	headersOut *httpnet.HTTPHeaderList

	asyncCallback func(hra *HTTPReqAux, opaque any, error int)
	asyncOpaque   any

	result    *misc.Buf
	resultPtr **misc.Buf // C: caller's buf_t ** for HTTP_TAG_RESULT_PTR

	httpCodePtr *int
}

// hraGzip — C: hra->zstream + inflateInit2(16+MAX_WBITS). The C code
// pushes compressed blocks through inflate(); Go drives a gzip.Reader
// over an io.Pipe so decoded_data callbacks keep their incremental
// delivery semantics.
type hraGzip struct {
	pw   *io.PipeWriter
	done chan int
}

// appendGzip — C: append_gzip.
func appendGzip(hf *httpFile, hra *HTTPReqAux, data []byte) int {
	gz := hra.gzip
	if len(data) == 0 {
		// EOF: close the writer and wait for the decoder to finish.
		gz.pw.Close()
		return <-gz.done
	}
	if _, err := gz.pw.Write(data); err != nil {
		// decoder aborted (decoded_data error or zlib error)
		select {
		case rc := <-gz.done:
			_ = rc
		default:
		}
		return -1
	}
	return 0
}

// gzipDecodeLoop — runs in a goroutine; C: the inflate() loop inside
// append_gzip.
func gzipDecodeLoop(pr *io.PipeReader, hf *httpFile, hra *HTTPReqAux,
	done chan int) {
	gr, err := gzip.NewReader(pr)
	if err != nil {
		done <- -1
		return
	}
	tmp := make([]byte, 8192)
	for {
		n, err := gr.Read(tmp)
		if n > 0 {
			if hra.decodedData(hf, hra, tmp[:n]) != 0 {
				pr.CloseWithError(errHTTPRead)
				done <- -1
				return
			}
		}
		if err != nil {
			rc := 0
			if errors.Is(err, io.EOF) {
				// Z_STREAM_END → final decoded_data(NULL, 0)
				if hra.decodedData(hf, hra, nil) != 0 {
					rc = -1
				}
			} else {
				setErrbuf(hra.errbuf,
					fmt.Sprintf("zlib error %v", err))
				rc = -1
			}
			done <- rc
			return
		}
	}
}

// httpRecvChunked — C: http_recv_chunked.
func httpRecvChunked(hf *httpFile, hra *HTTPReqAux) int {
	var chunkheader [100]byte
	hc := hf.connection

	for {
		if hc.tc.TCPReadLine(chunkheader[:]) < 0 {
			return -2
		}
		remain := int(strtolHex(misc.CStr(chunkheader[:])))
		if remain == 0 {
			// Trailing \r\n-line
			if hc.tc.TCPReadLine(chunkheader[:]) < 0 {
				return -2
			}
			break
		}

		for remain > 0 {
			rsize := min(remain, httpTmpSize)
			if hc.tc.TCPReadData(hra.tmpbuf[:rsize],
				httpRequestPartial, hra) != 0 {
				return -2
			}
			if hra.encodedData(hf, hra, hra.tmpbuf[:rsize]) != 0 {
				return -1
			}
			hra.bytesCompleted += int64(rsize)
			remain -= rsize
		}

		if hc.tc.TCPReadData(chunkheader[:2], nil, nil) != 0 {
			return -2
		}
	}
	hf.rsize = 0
	return hra.encodedData(hf, hra, nil)
}

// httpRecvUntilEOF — C: http_recv_until_eof.
func httpRecvUntilEOF(hf *httpFile, hra *HTTPReqAux) int {
	hc := hf.connection
	for {
		r := hc.tc.TCPReadDataNowait(hra.tmpbuf[:httpTmpSize])
		if r < 0 {
			break
		}
		if hra.encodedData(hf, hra, hra.tmpbuf[:r]) != 0 {
			return -1
		}
		hra.bytesCompleted += int64(r)
		if r == 0 {
			break
		}
	}
	return hra.encodedData(hf, hra, nil)
}

// httpRecv — C: http_recv (fixed content-length).
func httpRecv(hf *httpFile, hra *HTTPReqAux) int {
	hc := hf.connection
	remain := hf.rsize

	for remain > 0 {
		rsize := min(remain, httpTmpSize)
		if hc.tc.TCPReadData(hra.tmpbuf[:rsize],
			httpRequestPartial, hra) != 0 {
			return -2
		}
		if hra.encodedData(hf, hra, hra.tmpbuf[:rsize]) != 0 {
			return -1
		}
		hra.bytesCompleted += int64(rsize)
		remain -= rsize
	}
	hf.rsize = 0
	return hra.encodedData(hf, hra, hra.tmpbuf[:0])
}

// httpReqDo — C: http_req_do.
func httpReqDo(hra *HTTPReqAux) int {
	var q misc.HtsbufQueue
	var code, r int
	r = -1
	redircount := 0
	hf := hra.hf

retry:
	if err := httpConnect(hf, !hra.post, 2); err != nil {
		setErrbuf(hra.errbuf, err.Error())
		goto cleanup
	}
	{
		hc := hf.connection
		q.HtsbufQueueSetup(0)

		m := hra.method
		if m == "" {
			if hra.post {
				m = "POST"
			} else if hra.wantResult {
				m = "GET"
			} else {
				m = "HEAD"
			}
		}

		q.Append([]byte(m))
		q.Append([]byte(" "))
		q.Append([]byte(hf.path))

		// If the path already contains a '?' the first parameter is
		// prefixed with '&'
		prefix := byte('?')
		if strings.Contains(hf.path, "?") {
			prefix = '&'
		}

		if hra.arguments != nil {
			args := hra.arguments
			for len(args) > 0 && args[0] != "" {
				if len(args) > 1 && args[1] != "" {
					q.Append([]byte{prefix})
					q.AppendAndEscapeURL(args[0])
					q.Append([]byte("="))
					q.AppendAndEscapeURL(args[1])
					prefix = '&'
				}
				args = args[2:]
			}
		}

		for _, hqa := range hra.queryArgs {
			q.Append([]byte{prefix})
			q.AppendAndEscapeURL(hqa.key)
			q.Append([]byte("="))
			q.AppendAndEscapeURLLen(hqa.val, hqa.valLen)
			prefix = '&'
		}

		q.QPrintf(" HTTP/1.%d\r\n", hf.version)

		var headers httpnet.HTTPHeaderList
		httpHeadersSetup(&headers, hf)

		if hra.post {
			headers.AddInt("Content-Length", hra.postdata.Len())
		}
		if hra.postContentType != "" {
			headers.Add("Content-Type", hra.postContentType, false)
		}

		var cookies httpnet.HTTPHeaderList
		if hra.flags&FaDisableAuth == 0 {
			if err := httpRequestInspect(&headers, &cookies, hf, m,
				hra.arguments); err != nil {
				setErrbuf(hra.errbuf, err.Error())
				q.Flush()
				r = -1
				goto cleanup
			}
		}

		httpCookieAppend(hc.hostname, hf, &headers, &cookies)
		cookies.Free()

		hf.hfTrace("Sending request for %s (cid=%d)", hf.url, hc.id)
		httpHeadersSend(&q, &headers, &hra.headersIn)

		if hf.debug {
			traceRequest(&q, hf)
		}

		hf.connection.tc.TCPWriteQueue(&q)

		if hra.post {
			if hf.debug {
				hf.hfTrace("HTTP-POSTDATA: %d bytes", hra.postdata.Len())
			}
			hf.connection.tc.TCPWriteQueueDontfree(&hra.postdata)
		}

		code = httpReadResponse(hf, hra.headersOut)
		if code == -1 && hf.connection.reused {
			httpDetach(hf, false, "Read error on reused connection")
			goto retry
		}

		noContent := m == "HEAD"

		if hra.httpCodePtr != nil {
			*hra.httpCodePtr = code
		}

		switch code {
		case 204:
			noContent = true
			fallthrough
		case 200, 201, 202, 203, 205:
			if noContent {
				hf.rsize = 0
			}

		case 304:
			// Not modified
			httpDrainContent(hf)
			httpDestroy(hf)
			if hra.decodedCleanup != nil {
				hra.decodedCleanup(hra)
			}
			return 304

		case 302, 303:
			hra.post = false
			hra.postContentType = ""
			if hra.wantResult {
				hra.method = "GET"
			} else {
				hra.method = "HEAD"
			}
			fallthrough
		case 301, 307:
			if hra.flags&FaNofollow != 0 {
				hf.hfTrace("Not following redirect as requested by caller")
				break
			}
			if err := httpRedirectF(hf, &redircount, code,
				!noContent); err != nil {
				setErrbuf(hra.errbuf, err.Error())
				goto cleanup
			}
			goto retry

		case 401:
			var statcode int
			var ni *int
			if hra.flags&FaNonInteractive != 0 {
				ni = &statcode
			}
			if err := httpAuthenticate(hf, ni, !noContent); err != nil {
				setErrbuf(hra.errbuf, err.Error())
				goto cleanup
			}
			goto retry

		case 206:
			// "Partial Content" without asking — some servers
			// (FlashCom/3.5.7) remember Range requests on the same
			// connection. Redo the request.
			httpDetach(hf, false, "Got 206 without asking for it")
			goto retry

		default:
			if hra.flags&FaContentOnError != 0 && hf.rsize != 0 &&
				code > 0 {
				hf.hfTrace("%s failed with %d but content is available",
					hf.url, code)
				break
			}
			setErrbuf(hra.errbuf, fmt.Sprintf("HTTP error: %d", code))
			if !noContent && httpDrainContent(hf) != 0 {
				hf.connectionMode = connModeClose
			}
			goto cleanup
		}

		if !noContent {
			if hf.contentEncoding == httpCEGzip {
				pr, pw := io.Pipe()
				hra.gzip = &hraGzip{pw: pw, done: make(chan int, 1)}
				go gzipDecodeLoop(pr, hf, hra, hra.gzip.done)
				hra.encodedData = appendGzip
				hf.hfTrace("Inflating content using gzip")
			} else {
				hra.encodedData = hra.decodedData
			}

			hra.tmpbuf = make([]byte, httpTmpSize)

			if hf.chunkedTransfer {
				hf.hfTrace("Chunked transfer")
				r = httpRecvChunked(hf, hra)
			} else if hf.rsize == -1 {
				hf.hfTrace("Reading data until EOF")
				r = httpRecvUntilEOF(hf, hra)
			} else {
				hf.hfTrace("Reading %d bytes", hf.rsize)
				hra.total = hf.rsize
				r = httpRecv(hf, hra)
			}

			if r == -2 {
				setErrbuf(hra.errbuf, "Network error")
				r = -1
			}

			if hf.contentEncoding == httpCEGzip {
				// C: inflateEnd(&hra->zstream)
				hra.gzip = nil
			}
		} else {
			hf.hfTrace("No data transfered")
			r = 0
		}
	}
cleanup:
	hra.tmpbuf = nil

	if r != 0 && hra.decodedCleanup != nil {
		hra.decodedCleanup(hra)
	}
	httpDestroy(hra.hf)
	return r
}

// HTTPReqRelease — C: http_req_release.
func HTTPReqRelease(hra *HTTPReqAux) {
	if atomic.AddInt32(&hra.refcount, -1) != 0 {
		return
	}
	hra.headersIn.Free()
	if hra.result != nil {
		hra.result.Release()
	}
}

// HTTPReqRetain — C: http_req_retain.
func HTTPReqRetain(hra *HTTPReqAux) *HTTPReqAux {
	atomic.AddInt32(&hra.refcount, 1)
	return hra
}

// httpReqAsync — C: http_req_async (task).
func httpReqAsync(opaque any) {
	hra := opaque.(*HTTPReqAux)
	r := httpReqDo(hra)
	if r != 0 {
		hra.result = nil
	}
	hra.asyncCallback(hra, hra.asyncOpaque, r)
	HTTPReqRelease(hra)
}

// HTTPReqGetResult — C: http_req_get_result.
func HTTPReqGetResult(hra *HTTPReqAux) *misc.Buf {
	return hra.result
}
