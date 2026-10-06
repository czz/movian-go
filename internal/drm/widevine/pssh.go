// pssh.go — rebuild a complete ISO BMFF 'pssh' box from the fields
// FFmpeg surfaces via AVEncryptionInitInfo (system_id, key_ids, data)
// when the raw box bytes are not propagated. gowidevine's NewPSSH
// decodes the full box, so it must be re-serialized here.
package widevine

import (
	"encoding/binary"

	"github.com/czz/movian-go/internal/drm/cenc"
)

// marshalPSSH serializes p as a 'pssh' full box: v1 with the KID list
// when KIDs are present, v0 otherwise. The key-system data rides in
// the data section either way.
func marshalPSSH(p *cenc.PSSHBox) []byte {
	version := byte(0)
	if len(p.KIDs) > 0 {
		version = 1
	}
	body := 4 + 16 + 4 + len(p.Data)
	if version == 1 {
		body += 4 + 16*len(p.KIDs)
	}
	box := make([]byte, 8, 8+body)
	binary.BigEndian.PutUint32(box[:4], uint32(8+body))
	copy(box[4:8], "pssh")
	box = append(box, version, 0, 0, 0)
	box = append(box, p.SystemID[:]...)
	if version == 1 {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(p.KIDs)))
		box = append(box, n[:]...)
		for _, k := range p.KIDs {
			box = append(box, k[:]...)
		}
	}
	var ds [4]byte
	binary.BigEndian.PutUint32(ds[:], uint32(len(p.Data)))
	box = append(box, ds[:]...)
	return append(box, p.Data...)
}
