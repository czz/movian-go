// Package pipe is the stateful decrypt stage that sits between
// av_read_frame and avcodec_send_packet: it collects
// ENCRYPTION_INIT_INFO side data, lazily opens CDM sessions via
// pkg/drm/cdm, and decrypts samples in place.
// New in Go — no C counterpart.
package pipe

import (
	"fmt"
	"sync"

	"github.com/czz/movian-go/internal/drm/cdm"
	"github.com/czz/movian-go/internal/drm/cenc"
)

// Decryptor is bound to one playback pipeline (audio and video
// decoders may share one instance so a single license exchange
// covers both tracks).
type Decryptor struct {
	mu       sync.Mutex
	sessions map[[16]byte]cdm.Session // key system -> session
	pending  []pendingLicense         // challenges awaiting HTTP post
}

// New returns an empty Decryptor.
func New() *Decryptor {
	return &Decryptor{sessions: map[[16]byte]cdm.Session{}}
}

// AttachInitInfo feeds pssh declarations found on a packet
// (ENCRYPTION_INIT_INFO side data) and opens sessions as needed.
// Returns license requests the caller must post to the license
// servers — empty for ClearKey and already-usable sessions.
func (d *Decryptor) AttachInitInfo(pssh *cenc.PSSHBox) (*cdm.LicenseRequest, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.sessions[pssh.SystemID]; ok {
		return nil, nil
	}
	sys, err := cdm.ForSystemID(pssh.SystemID)
	if err != nil {
		return nil, err // key system unsupported — playback fails clearly
	}
	s, err := sys.CreateSession(pssh)
	if err != nil {
		return nil, err
	}
	d.sessions[pssh.SystemID] = s
	req := s.LicenseRequest()
	if req != nil {
		d.pending = append(d.pending, pendingLicense{sess: s, req: req})
	}
	return req, nil
}

// DecryptPacket decrypts one sample in place. inits are the init-info
// declarations seen on the packet (may be nil). Returns license
// requests to be fulfilled when a session must be created.
func (d *Decryptor) DecryptPacket(sample []byte, info *cenc.SampleInfo, inits []*cenc.PSSHBox) (*cdm.LicenseRequest, error) {
	var req *cdm.LicenseRequest
	for _, p := range inits {
		r, err := d.AttachInitInfo(p)
		if err != nil {
			return nil, err
		}
		if r != nil {
			req = r
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sessions {
		if !s.Usable() {
			continue
		}
		if err := s.DecryptSample(sample, info); err == nil {
			return req, nil
		}
	}
	if len(d.sessions) == 0 {
		return nil, fmt.Errorf("drm: encrypted packet but no key system initialized")
	}
	return nil, fmt.Errorf("drm: no usable session could decrypt packet")
}

// PendingRequests returns and clears queued license challenges.
// Callers that intend to post them should prefer FulfillPending,
// which also routes each response back into its session.
func (d *Decryptor) PendingRequests() []*cdm.LicenseRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*cdm.LicenseRequest
	for _, p := range d.pending {
		out = append(out, p.req)
	}
	d.pending = nil
	return out
}

// Close releases all sessions.
func (d *Decryptor) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sessions {
		s.Close()
	}
	d.sessions = map[[16]byte]cdm.Session{}
}
