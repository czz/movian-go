// Package clearkey implements the W3C Clear Key key system:
// keys arrive as a JSON Web Key license (EME Clear Key) or are
// pre-provisioned by the host. Decryption runs through pkg/drm/cenc.
// New in Go — no C counterpart.
package clearkey

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/czz/movian-go/internal/drm/cdm"
	"github.com/czz/movian-go/internal/drm/cenc"
)

// jwkLicense is the EME Clear Key license format:
// {"keys":[{"kty":"oct","k":"<b64url key>","kid":"<b64url kid>"}],"type":"temporary"}
type jwkLicense struct {
	Keys []struct {
		Kty string `json:"kty"`
		K   string `json:"k"`
		Kid string `json:"kid"`
	} `json:"keys"`
}

func b64u(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// ClearKey is a cdm.CDM; keys can also be injected via SetKey.
type ClearKey struct {
	mu   sync.Mutex
	keys map[[16]byte][]byte // KID -> AES-128 key
}

// New returns an empty ClearKey CDM and registers it.
func New() *ClearKey {
	c := &ClearKey{keys: map[[16]byte][]byte{}}
	cdm.Register(c)
	return c
}

// SetKey pre-provisions a content key (e.g. from a manifest or plugin).
func (c *ClearKey) SetKey(kid [16]byte, key []byte) error {
	if len(key) != 16 {
		return fmt.Errorf("clearkey: key must be 16 bytes")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys[kid] = key
	return nil
}

// SystemID implements cdm.CDM.
func (c *ClearKey) SystemID() [16]byte { return cenc.SystemIDClearKey }

// CreateSession implements cdm.CDM. If the PSSH data is itself a JWK
// license, keys are absorbed immediately and no license request is
// needed (Usable() == true at once).
func (c *ClearKey) CreateSession(pssh *cenc.PSSHBox) (cdm.Session, error) {
	s := &session{parent: c, keys: map[[16]byte][]byte{}}
	if len(pssh.Data) > 0 && pssh.Data[0] == '{' {
		if err := s.Update(pssh.Data); err != nil {
			return nil, err
		}
	} else {
		// PSSH carries raw KIDs (v1 box or 16-byte data): pre-stage them.
		for _, k := range pssh.KIDs {
			s.kids = append(s.kids, k)
		}
		if len(pssh.Data) == 16 {
			var k [16]byte
			copy(k[:], pssh.Data)
			s.kids = append(s.kids, k)
		}
	}
	return s, nil
}

type session struct {
	parent *ClearKey
	keys   map[[16]byte][]byte // session-level keys (from Update)
	kids   [][16]byte
}

// LicenseRequest implements cdm.Session — nil: ClearKey has no server
// round-trip; keys arrive via Update (JWK license) or SetKey.
func (s *session) LicenseRequest() *cdm.LicenseRequest { return nil }

// Update parses a JWK license and installs its keys.
func (s *session) Update(response []byte) error {
	var l jwkLicense
	if err := json.Unmarshal(response, &l); err != nil {
		return fmt.Errorf("clearkey: bad JWK license: %w", err)
	}
	for _, jk := range l.Keys {
		if jk.Kty != "oct" {
			continue
		}
		kb, err1 := b64u(jk.K)
		ib, err2 := b64u(jk.Kid)
		if err1 != nil || err2 != nil || len(kb) != 16 || len(ib) != 16 {
			return fmt.Errorf("clearkey: malformed key entry")
		}
		var kid [16]byte
		copy(kid[:], ib)
		s.keys[kid] = kb
	}
	return nil
}

func (s *session) Usable() bool { return len(s.keys) > 0 || len(s.parent.keys) > 0 }

func (s *session) Close() { s.keys = map[[16]byte][]byte{} }

// DecryptSample implements cdm.Session.
func (s *session) DecryptSample(sample []byte, info *cenc.SampleInfo) error {
	var kid [16]byte
	copy(kid[:], info.KeyID)
	key, ok := s.keys[kid]
	if !ok {
		s.parent.mu.Lock()
		key, ok = s.parent.keys[kid]
		s.parent.mu.Unlock()
	}
	if !ok {
		return fmt.Errorf("clearkey: no key for KID %x", kid)
	}
	return cenc.DecryptSample(sample, sample, key, info)
}
