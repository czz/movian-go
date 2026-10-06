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
	"time"

	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	propcore "github.com/czz/movian-go/internal/prop"
)

// C: SEEK_BY_READ_THRES — forward seeks below this delta are satisfied by
// reading (and dropping) bytes on a pushed stream.
const seekByReadThres = 256 * 1024

// C: STREAMING_LIMIT — after this many consecutive read bytes the file
// switches to a continuous stream instead of range requests.
const streamingLimit = 128000

// C: HTTP_TMP_SIZE — receive scratch size for http_recv*.
const httpTmpSize = 16384

// C: http_file_t.hf_connection_mode
const (
	connModePersistent = iota // CONNECTION_MODE_PERSISTENT
	connModeClose             // CONNECTION_MODE_CLOSE
)

// C: http_file_t.hf_content_encoding
const (
	httpCEIdentity = 0 // HTTP_CE_IDENTITY
	httpCEGzip     = 1 // HTTP_CE_GZIP
)

// C: URL_MAX
// urlMax — C: URL_MAX (2048). Raised to 4096: Googlevideo signed
// manifest/segment URLs routinely exceed 2048 bytes and the path copy
// silently truncates — dropping trailing sig/lsig params → HTTP 403.
const urlMax = 4096

var errHTTPRead = errors.New("http read error")

// HTTPRequestInspection — C: http_request_inspection_t.
type HTTPRequestInspection struct {
	Method        string                  // hri_method
	Parameters    []string                // hri_parameters (k/v pairs)
	HF            *httpFile               // hri_hf
	Headers       *httpnet.HTTPHeaderList // hri_headers
	Cookies       *httpnet.HTTPHeaderList // hri_cookies
	Errbuf        []byte                  // hri_errbuf
	ForceFail     bool                    // hri_force_fail
	AuthHasFailed int                     // hri_auth_has_failed
}

// HTTPRequestInspectorCheck — C: http_request_inspector_t.check.
// Returns false to veto the request (hri.ForceFail decides error vs retry).
type HTTPRequestInspectorCheck func(url string, hri *HTTPRequestInspection) bool

// HTTPRequestInspector — C: http_request_inspector_t.
type HTTPRequestInspector struct {
	Check HTTPRequestInspectorCheck
}

// httpFile — C: http_file_t.
type httpFile struct {
	fam         *FileAccessManager
	connection  *httpConnection
	url         string
	auth        string
	location    string
	authRealm   string
	retLocation *string // C: hf_ret_location
	authurl     string  // C: hf_authurl[128]
	path        string  // C: hf_path[URL_MAX]

	statusCode int
	chunkSize  int

	rsize    int64 // C: hf_rsize — pending reply bytes (-1 until EOF)
	filesize int64 // C: hf_filesize (-1 unknown)
	pos      int64

	consecutiveRead int64

	contentType string

	connectionMode int
	mtime          time.Time

	chunkedTransfer bool
	isdir           bool
	authFailed      int
	extAuth         bool
	debug           bool
	sslVerify       bool
	noRanges        bool
	wantClose       bool
	acceptRanges    bool
	version         int
	streaming       bool
	noRetries       bool
	noCookies       bool
	reqCompression  bool
	originalURL     string
	filesizeIsFinal bool
	contentEncoding int
	maxAge          int
	connectTimeout  int
	readTimeout     int
	statsSpeed      *propcore.Prop
	userReqHeaders  *httpnet.HTTPHeaderList // C: hf_user_request_headers
	userRespHeaders *httpnet.HTTPHeaderList // C: hf_user_response_headers
	cancellable     *misc.Cancellable
	bytesDownloaded uint64
	downloadRate    misc.Average
	id              int

	err error // sticky io error for the io.Reader interface
}

type httpRedirect struct {
	from string
	to   string
}

// HTTP_TAG_* — C: http_client.h tag enum.
const (
	HTTPTagArg = iota + 1
	HTTPTagArgInt
	HTTPTagArgInt64
	HTTPTagArgBin
	HTTPTagArgList
	HTTPTagResultPtr
	HTTPTagErrbuf
	HTTPTagPostData
	HTTPTagFlags
	HTTPTagRequestHeader
	HTTPTagRequestHeaders
	HTTPTagResponseHeaders
	HTTPTagMethod
	HTTPTagProgressCallback
	HTTPTagCancellable
	HTTPTagConnectTimeout
	HTTPTagReadTimeout
	HTTPTagLocation
	HTTPTagResponseCode
)
