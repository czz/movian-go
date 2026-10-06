// Package cdm defines the Content Decryption Module abstraction for
// movian-go: one Session per protected stream, a license exchange
// (GenerateRequest/Update) and per-sample Decrypt.
// New in Go — upstream Movian C has no DRM support; the interface is
// modelled on the Chromium CDM API (content_decryption_module.h) so
// that both ClearKey (native) and Widevine (external blob) fit.
package cdm

import (
	"fmt"
	"sync"

	"github.com/czz/movian-go/internal/drm/cenc"
)

// LicenseRequest is a challenge produced by CreateSession that must be
// sent to the key system's license server.
type LicenseRequest struct {
	Type string // "license-request" | "license-renewal" | "individualization"
	URL  string // optional default license URL (from init data)
	Data []byte // opaque challenge blob
}

// Session is one key-system session over a stream's KID set.
type Session interface {
	// LicenseRequest returns the pending challenge, or nil if the
	// session is already usable (e.g. ClearKey with inline keys).
	LicenseRequest() *LicenseRequest
	// Update feeds a license response; called after the request is
	// posted to the license server.
	Update(response []byte) error
	// Usable reports whether content keys are available for Decrypt.
	Usable() bool
	// DecryptSample decrypts one sample in place. info is the
	// per-sample encryption metadata (IV, subsamples).
	DecryptSample(sample []byte, info *cenc.SampleInfo) error
	// Close releases the session.
	Close()
}

// CDM is a key-system implementation factory.
type CDM interface {
	// SystemID is the 16-byte ISO 23001-7 identifier this CDM handles
	// (Widevine, PlayReady, ClearKey, ...).
	SystemID() [16]byte
	// CreateSession starts a session from a 'pssh' init-data blob.
	CreateSession(pssh *cenc.PSSHBox) (Session, error)
}

var (
	mu       sync.RWMutex
	registry = map[[16]byte]CDM{}
)

// Register installs a CDM for its system ID. Called from init() of the
// concrete key-system packages (pkg/drm/clearkey, pkg/drm/widevine).
func Register(c CDM) {
	mu.Lock()
	defer mu.Unlock()
	registry[c.SystemID()] = c
}

// ForSystemID looks up the CDM handling the given system ID.
func ForSystemID(id [16]byte) (CDM, error) {
	mu.RLock()
	defer mu.RUnlock()
	if c, ok := registry[id]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("cdm: no key system registered for %x", id)
}
