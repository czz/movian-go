// encryption.go — bridge between AVPacket ENCRYPTION_INFO side data
// and pkg/drm/cenc.SampleInfo. New in Go — no C counterpart (upstream
// Movian has no DRM support). FFmpeg's mov/dash demuxers attach
// AV_PKT_DATA_ENCRYPTION_INFO to every cenc-encrypted sample when no
// internal decryption_key is configured.
package libav

/*
#include <libavcodec/avcodec.h>
#include <libavcodec/packet.h>
#include <libavutil/encryption_info.h>
#include <stdlib.h>
#include <string.h>

// ml_packet_encryption_info — fetch + decode the ENCRYPTION_INFO side
// data of an AVPacket. Returns the parsed struct, or NULL when the
// packet carries no encryption info. Caller frees via
// av_encryption_info_free.
static AVEncryptionInfo *ml_packet_encryption_info(void *pkt) {
    AVPacket *p = (AVPacket *)pkt;
    size_t sz = 0;
    uint8_t *sd = av_packet_get_side_data(p, AV_PKT_DATA_ENCRYPTION_INFO, &sz);
    if (sd == NULL || sz == 0) {
        return NULL;
    }
    return av_encryption_info_get_side_data(sd, sz);
}

// ml_packet_encryption_init_info — ENCRYPTION_INIT_INFO side data:
// list head of AVEncryptionInitInfo (or NULL). Caller frees via
// av_encryption_init_info_free.
static AVEncryptionInitInfo *ml_packet_encryption_init_info(void *pkt) {
    AVPacket *p = (AVPacket *)pkt;
    size_t sz = 0;
    uint8_t *sd = av_packet_get_side_data(p, AV_PKT_DATA_ENCRYPTION_INIT_INFO, &sz);
    if (sd == NULL || sz == 0) {
        return NULL;
    }
    return av_encryption_init_info_get_side_data(sd, sz);
}

// ml_packet_data — raw payload pointer/size of an AVPacket for
// in-place decryption before avcodec_send_packet.
static uint8_t *ml_packet_data(void *pkt) {
    return ((AVPacket *)pkt)->data;
}
static int ml_packet_size(void *pkt) {
    return ((AVPacket *)pkt)->size;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/czz/movian-go/internal/drm/cenc"
	"github.com/czz/movian-go/internal/drm/pipe"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
)

// PacketEncryptionInfo extracts the per-sample encryption metadata of
// a raw *C.AVPacket (as returned by ReadPacketRaw). nil when the
// packet is in the clear.
func PacketEncryptionInfo(pkt *libav.AVPacket) *cenc.SampleInfo {
	ei := C.ml_packet_encryption_info(pkt.CPtr())
	if ei == nil {
		return nil
	}
	defer C.av_encryption_info_free(ei)

	info := &cenc.SampleInfo{
		Scheme:         cenc.Scheme(ei.scheme),
		CryptByteBlock: uint32(ei.crypt_byte_block),
		SkipByteBlock:  uint32(ei.skip_byte_block),
	}
	info.KeyID = C.GoBytes(unsafe.Pointer(ei.key_id), C.int(ei.key_id_size))
	info.IV = C.GoBytes(unsafe.Pointer(ei.iv), C.int(ei.iv_size))
	n := int(ei.subsample_count)
	if n > 0 && ei.subsamples != nil {
		info.Subsamples = make([]cenc.Subsample, n)
		sz := unsafe.Sizeof(C.AVSubsampleEncryptionInfo{})
		for i := range n {
			s := (*C.AVSubsampleEncryptionInfo)(
				unsafe.Add(unsafe.Pointer(ei.subsamples), uintptr(i)*sz))
			info.Subsamples[i] = cenc.Subsample{
				Clear:     uint32(s.bytes_of_clear_data),
				Protected: uint32(s.bytes_of_protected_data),
			}
		}
	}
	return info
}

// PacketEncryptionInitInfos parses the ENCRYPTION_INIT_INFO side data
// of a raw *C.AVPacket into cenc.PSSHBox values (one per key system
// declared by the demuxer). nil when absent.
func PacketEncryptionInitInfos(pkt *libav.AVPacket) []*cenc.PSSHBox {
	ei := C.ml_packet_encryption_init_info(pkt.CPtr())
	if ei == nil {
		return nil
	}
	defer C.av_encryption_init_info_free(ei)

	var out []*cenc.PSSHBox
	for cur := ei; cur != nil; cur = cur.next {
		b := &cenc.PSSHBox{}
		copy(b.SystemID[:], C.GoBytes(unsafe.Pointer(cur.system_id),
			C.int(cur.system_id_size)))
		n := int(cur.num_key_ids)
		ksz := int(cur.key_id_size)
		for i := 0; i < n && cur.key_ids != nil; i++ {
			kp := *(**C.uint8_t)(unsafe.Add(
				unsafe.Pointer(cur.key_ids), uintptr(i)*unsafe.Sizeof(uintptr(0))))
			var k [16]byte
			copy(k[:], C.GoBytes(unsafe.Pointer(kp), C.int(ksz)))
			b.KIDs = append(b.KIDs, k)
		}
		if cur.data != nil && cur.data_size > 0 {
			b.Data = C.GoBytes(unsafe.Pointer(cur.data), C.int(cur.data_size))
		}
		out = append(out, b)
	}
	return out
}

// PacketData returns the raw payload of a raw *C.AVPacket as a Go
// slice aliasing the packet buffer — for in-place decryption before
// avcodec_send_packet. nil for empty packets.
func PacketData(pkt *libav.AVPacket) []byte {
	p := C.ml_packet_data(pkt.CPtr())
	n := C.ml_packet_size(pkt.CPtr())
	if p == nil || n <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(p)), int(n))
}

// ErrLicensePending is returned by DRMDecryptPacket when a license
// exchange could not be completed (no resolvable license URL) — the
// sample could not be decrypted.
var ErrLicensePending = errors.New("drm: license request pending")

// DRMDecryptPacket decrypts a raw *C.AVPacket in place when it carries
// cenc encryption metadata. It lazily creates the codec's pipe.Decryptor
// (mc.DRM), absorbs ENCRYPTION_INIT_INFO declarations into CDM sessions
// and decrypts ENCRYPTION_INFO samples before avcodec_send_packet.
// Clear packets pass through with nil.
//
// When a session emits a license request, the exchange runs inline on
// the decode thread (resolve URL → POST challenge → Update) and the
// packet is retried once — the same blocking model Kodi uses.
func DRMDecryptPacket(mc *mediacore.MediaCodec, pkt *libav.AVPacket) error {
	inits := PacketEncryptionInitInfos(pkt)
	info := PacketEncryptionInfo(pkt)
	if inits == nil && info == nil {
		return nil
	}
	d, _ := mc.DRM.(*pipe.Decryptor)
	if d == nil {
		d = pipe.New()
		mc.DRM = d
	}
	req, err := d.DecryptPacket(PacketData(pkt), info, inits)
	if req != nil {
		if ferr := fulfillLicense(mc, d); ferr != nil {
			return ferr
		}
		req, err = d.DecryptPacket(PacketData(pkt), info, nil)
		if req != nil {
			return ErrLicensePending
		}
	}
	return err
}

// fulfillLicense resolves the license endpoint for the codec's stream
// (mc.MP.URL — "drm_license" query param or the pipe.Resolver hook)
// and completes all pending exchanges on d.
func fulfillLicense(mc *mediacore.MediaCodec, d *pipe.Decryptor) error {
	streamURL := ""
	if mc.MP != nil {
		streamURL = mc.MP.URL
	}
	licenseURL, headers, ok := pipe.LicenseURL(streamURL)
	if !ok {
		return fmt.Errorf("drm: license request pending but no license URL " +
			"(append ?drm_license=... to the stream URL or wire pipe.SetLicenseResolver)")
	}
	return d.FulfillPending(licenseURL, headers)
}
