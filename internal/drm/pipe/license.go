// license.go — license server round-trip for pending CDM sessions:
// resolves the license endpoint, POSTs the challenge and feeds the
// response back into the session via Update. New in Go — no C
// counterpart.
package pipe

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/czz/movian-go/internal/drm/cdm"
)

// pendingLicense pairs a queued challenge with the session that must
// consume the response.
type pendingLicense struct {
	sess cdm.Session
	req  *cdm.LicenseRequest
}

// licenseResolver maps a stream URL to a license endpoint when the
// "drm_license" query parameter is absent — assigned by the
// settings/plugin layer via SetLicenseResolver. nil means only the
// URL parameter works.
var licenseResolver func(streamURL string) (licenseURL string, headers map[string]string, ok bool)

// SetLicenseResolver wires the license-endpoint resolver seam.
func SetLicenseResolver(
	fn func(streamURL string) (licenseURL string, headers map[string]string, ok bool)) {
	licenseResolver = fn
}

// LicenseURL resolves the license endpoint for a stream URL.
// Resolution order: the "?drm_license=" query parameter, the Resolver
// hook (settings/plugins), then automatic manifest fetch — a DASH MPD
// is scanned for ContentProtection Laurl elements, an HLS playlist for
// Widevine EXT-X-KEY/SESSION-KEY URIs.
func LicenseURL(streamURL string) (licenseURL string, headers map[string]string, ok bool) {
	if u, err := url.Parse(streamURL); err == nil {
		if lu := u.Query().Get("drm_license"); lu != "" {
			return lu, nil, true
		}
	}
	if licenseResolver != nil {
		if lu, h, ok := licenseResolver(streamURL); ok && lu != "" {
			return lu, h, true
		}
	}
	if lu, ok := manifestLicenseURL(streamURL); ok {
		return lu, nil, true
	}
	return "", nil, false
}

// FulfillPending posts every pending license challenge to licenseURL
// (requests carrying their own URL keep it) and feeds each response
// into its session via Update. The pending list is consumed either
// way; the first error is returned.
func (d *Decryptor) FulfillPending(licenseURL string, headers map[string]string) error {
	d.mu.Lock()
	pend := d.pending
	d.pending = nil
	d.mu.Unlock()

	var firstErr error
	for _, p := range pend {
		u := p.req.URL
		if u == "" {
			u = licenseURL
		}
		resp, err := postLicense(u, headers, p.req.Data)
		if err == nil {
			err = p.sess.Update(resp)
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("drm: license exchange: %w", err)
		}
	}
	return firstErr
}

// postLicense sends one license challenge as a raw POST body.
func postLicense(u string, headers map[string]string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	cl := &http.Client{Timeout: 15 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("license server: HTTP %d", resp.StatusCode)
	}
	return data, nil
}
