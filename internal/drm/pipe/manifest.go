// manifest.go — automatic license URL discovery: when neither the
// "drm_license" stream-URL parameter nor the Resolver hook produces an
// endpoint, the stream manifest is fetched and parsed. DASH MPDs are
// scanned for the ContentProtection license-URL elements
// (dashif:Laurl, mspr:laurl, clearkey:Laurl — same local name, any
// namespace); HLS playlists are scanned for EXT-X-SESSION-KEY /
// EXT-X-KEY with a Widevine KEYFORMAT, whose URI is the license URL
// per the Widevine HLS spec. New in Go — no C counterpart.
package pipe

import (
	"bufio"
	"encoding/xml"
	"net/http"
	"strings"
	"time"
)

const manifestFetchTimeout = 10 * time.Second

// manifestLicenseURL fetches the stream URL and extracts a license
// endpoint from it when the body is a DASH MPD or an HLS playlist.
func manifestLicenseURL(streamURL string) (string, bool) {
	if !strings.HasPrefix(streamURL, "http://") &&
		!strings.HasPrefix(streamURL, "https://") {
		return "", false
	}
	cl := &http.Client{Timeout: manifestFetchTimeout}
	resp, err := cl.Get(streamURL)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}

	// Manifests are small; cap anyway.
	const maxManifest = 4 << 20
	lr := http.MaxBytesReader(nil, resp.Body, maxManifest)
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "dash") {
		return mpdLicenseURL(xml.NewDecoder(lr))
	}
	if strings.Contains(ct, "mpegurl") || strings.Contains(ct, "m3u") {
		return hlsLicenseURL(bufio.NewReader(lr))
	}
	// Content-Type is unreliable for manifests — sniff the payload.
	br := bufio.NewReader(lr)
	head, _ := br.Peek(64)
	if strings.Contains(string(head), "<MPD") || strings.Contains(string(head), "<?xml") {
		return mpdLicenseURL(xml.NewDecoder(br))
	}
	return hlsLicenseURL(br)
}

// mpdLicenseURL walks MPD XML and returns the first license URL
// element — the local name alone identifies it across the DASH-IF
// (Laurl) and Microsoft (laurl) namespaces.
func mpdLicenseURL(d *xml.Decoder) (string, bool) {
	for {
		tok, err := d.Token()
		if err != nil {
			return "", false
		}
		se, ok := tok.(xml.StartElement)
		if !ok || !strings.EqualFold(se.Name.Local, "laurl") {
			continue
		}
		var text string
		if err := d.DecodeElement(&text, &se); err != nil {
			return "", false
		}
		if u := strings.TrimSpace(text); strings.HasPrefix(u, "http") {
			return u, true
		}
	}
}

// hlsLicenseURL scans a playlist for EXT-X-SESSION-KEY / EXT-X-KEY tags
// whose KEYFORMAT is Widevine and returns the tag's URI attribute.
func hlsLicenseURL(br *bufio.Reader) (string, bool) {
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "#EXT-X-") {
			continue
		}
		if !strings.Contains(line, "KEY") ||
			!strings.Contains(strings.ToLower(line), "edef8ba9-79d6-4ace-a3ce-827dcd51d21ed") {
			continue
		}
		if u := tagAttr(line, "URI"); strings.HasPrefix(u, "http") {
			return u, true
		}
	}
	return "", false
}

// tagAttr extracts attr="value" from an HLS tag line.
func tagAttr(line, attr string) string {
	key := attr + `="`
	_, after, ok := strings.Cut(line, key)
	if !ok {
		return ""
	}
	rest := after
	if before, _, ok := strings.Cut(rest, "\""); ok {
		return before
	}
	return ""
}
