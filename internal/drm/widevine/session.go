// session.go — one Widevine license session: pending challenge,
// license response handling and per-sample content-key lookup.
package widevine

import (
	"fmt"
	"sync"

	wv "github.com/iyear/gowidevine"
	wvpb "github.com/iyear/gowidevine/widevinepb"

	"github.com/czz/movian-go/internal/drm/cdm"
	"github.com/czz/movian-go/internal/drm/cenc"
)

// session implements cdm.Session over a gowidevine license exchange.
type session struct {
	mu      sync.Mutex
	pending *cdm.LicenseRequest
	parse   func([]byte) ([]*wv.Key, error)
	keys    map[string][]byte // KID -> AES-128 content key
}

// LicenseRequest implements cdm.Session — the challenge produced by
// CreateSession, nil once Update() has consumed the license.
func (s *session) LicenseRequest() *cdm.LicenseRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

// Update implements cdm.Session — feeds the license server response;
// CONTENT keys are unwrapped and indexed by KID.
func (s *session) Update(response []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.parse == nil {
		return fmt.Errorf("widevine: session already updated")
	}
	keys, err := s.parse(response)
	if err != nil {
		return fmt.Errorf("widevine: license parse: %w", err)
	}
	for _, k := range keys {
		if k.Type == wvpb.License_KeyContainer_CONTENT && len(k.Key) == 16 {
			s.keys[string(k.ID)] = k.Key
		}
	}
	s.pending = nil
	return nil
}

// Usable implements cdm.Session — true once at least one content key
// has been installed.
func (s *session) Usable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys) > 0
}

// DecryptSample implements cdm.Session — looks up the content key by
// the sample's KID and decrypts in place via pkg/drm/cenc.
func (s *session) DecryptSample(sample []byte, info *cenc.SampleInfo) error {
	s.mu.Lock()
	key, ok := s.keys[string(info.KeyID)]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("widevine: no content key for KID %x", info.KeyID)
	}
	return cenc.DecryptSample(sample, sample, key, info)
}

// Close implements cdm.Session — drops all session keys.
func (s *session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = map[string][]byte{}
	s.parse = nil
}
