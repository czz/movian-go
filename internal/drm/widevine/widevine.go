// Package widevine implements the Widevine L3 key system as a
// pkg/drm/cdm.CDM, backed by github.com/iyear/gowidevine — a pure-Go
// implementation of the Widevine license protocol (SignedMessage /
// LicenseRequest exchange, RSA-PSS request signing, AES session key
// derivation). No proprietary CDM blob is hosted — deliberately
// unlike Kodi's inputstream addon. Device credentials are
// user-supplied: a .wvd keybox file or raw client_id+private key.
// Security level 3 only — no TEE/L1.
// New in Go — upstream Movian C has no DRM support.
package widevine

import (
	"fmt"
	"os"

	wv "github.com/iyear/gowidevine"
	wvpb "github.com/iyear/gowidevine/widevinepb"

	"github.com/czz/movian-go/internal/drm/cdm"
	"github.com/czz/movian-go/internal/drm/cenc"
)

// Widevine is a cdm.CDM holding one set of device credentials.
type Widevine struct {
	device *wv.Device
	cdm    *wv.CDM
}

// New builds the Widevine key system from device credentials and
// registers it with pkg/drm/cdm for SystemIDWidevine.
func New(src wv.DeviceSource) (*Widevine, error) {
	d, err := wv.NewDevice(src)
	if err != nil {
		return nil, fmt.Errorf("widevine: device: %w", err)
	}
	w := &Widevine{device: d, cdm: wv.NewCDM(d)}
	cdm.Register(w)
	return w, nil
}

// Open loads device credentials from a .wvd keybox file.
func Open(path string) (*Widevine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("widevine: open device: %w", err)
	}
	defer f.Close()
	return New(wv.FromWVD(f))
}

// SystemID implements cdm.CDM.
func (w *Widevine) SystemID() [16]byte { return cenc.SystemIDWidevine }

// CreateSession implements cdm.CDM. The resulting session always has
// a pending LicenseRequest — Widevine requires a server round-trip
// even when the PSSH embeds all KIDs.
func (w *Widevine) CreateSession(pssh *cenc.PSSHBox) (cdm.Session, error) {
	raw := pssh.Raw
	if len(raw) == 0 {
		// FFmpeg's ENCRYPTION_INIT_INFO carries system_id/kids/data
		// but not the raw box — rebuild it for gowidevine.
		raw = marshalPSSH(pssh)
	}
	box, err := wv.NewPSSH(raw)
	if err != nil {
		return nil, fmt.Errorf("widevine: bad pssh: %w", err)
	}
	challenge, parse, err := w.cdm.GetLicenseChallenge(
		box, wvpb.LicenseType_AUTOMATIC, false)
	if err != nil {
		return nil, fmt.Errorf("widevine: license challenge: %w", err)
	}
	return &session{
		pending: &cdm.LicenseRequest{
			Type: "license-request",
			Data: challenge,
		},
		parse: parse,
		keys:  map[string][]byte{},
	}, nil
}
