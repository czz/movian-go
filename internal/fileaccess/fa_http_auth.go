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
	"encoding/base64"
	"errors"
)

// HTTPClientRawauth — C: http_client_rawauth.
func HTTPClientRawauth(hri *HTTPRequestInspection, str string) int {
	hri.Headers.Add("Authorization", str, false)
	return 0
}

type httpAuthCache struct {
	hostname    string
	port        int
	credentials string
}

// httpAuthCacheSet — C: http_auth_cache_set.
func httpAuthCacheSet(hf *httpFile) {
	fam := hf.fam
	hostname := hf.connection.hostname
	port := hf.connection.port
	credentials := hf.auth

	fam.httpAuthMu.Lock()
	defer fam.httpAuthMu.Unlock()

	idx := -1
	for i, hac := range fam.httpAuthCaches {
		if hac.hostname == hostname && hac.port == port {
			idx = i
			break
		}
	}

	if credentials == "" {
		if idx >= 0 {
			fam.httpAuthCaches = append(
				fam.httpAuthCaches[:idx], fam.httpAuthCaches[idx+1:]...)
		}
	} else {
		if idx < 0 {
			fam.httpAuthCaches = append([]*httpAuthCache{{
				hostname: hostname, port: port,
			}}, fam.httpAuthCaches...)
			idx = 0
		}
		fam.httpAuthCaches[idx].credentials = credentials
	}
}

// authenticate — C: authenticate.
func httpAuthenticate(hf *httpFile, nonInteractive *int,
	expectContent bool) error {
	fam := hf.fam
	if hf.authFailed > 0 && nonInteractive != nil {
		*nonInteractive = FAP_NEED_AUTH
		return errors.New("Authentication required")
	}

	if hf.extAuth {
		// This request is handled by an external inspector — just retry.
		hf.authFailed++
		return nil
	}

	// C: char buf1[128]; snprintf(buf1, "%s @ %s", realm, hostname)
	buf1 := snprintf128("%s @ %s", hf.authRealm, hf.connection.hostname)

	if expectContent && httpDrainContent(hf) != 0 {
		hf.connectionMode = connModeClose
	}

	if hf.authRealm == "" {
		return errors.New("Authentication without realm")
	}

	var username, password string
	krflags := keyringShowRememberMe | keyringRememberMeSet
	if hf.authFailed > 0 {
		krflags |= keyringQueryUser
	}
	r := 1 // keyringNotFound
	if fam.keyringLookup != nil {
		r = fam.keyringLookup(buf1, &username, &password, nil, nil,
			"HTTP Client", "Access denied", krflags)
	}

	hf.authFailed++
	hf.auth = ""

	if r == -1 { // KEYRING_USER_REJECTED
		return errors.New("Authentication rejected by user")
	}

	if r == 0 { // KEYRING_OK
		hf.hfTrace("%s: Authenticating with %s %s", hf.url, username, password)
		// C: snprintf(buf1[128], "%s:%s") → av_base64_encode into
		// buf2[128] → snprintf(buf1[128], "Basic %s") — all truncating.
		up := snprintf128("%s:%s", username, password)
		b64 := base64.StdEncoding.EncodeToString([]byte(up))
		if len(b64) > 127 {
			b64 = b64[:127] // C: av_base64_encode into char[128]
		}
		hf.auth = snprintf128("Basic %s", b64)
		return nil
	}
	// No auth info
	return nil
}
