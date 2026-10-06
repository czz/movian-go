package core

// FourCC — C: uint32_t multi-character constants ('none', 'YUVP', ...)
// used as frame/codec type tags (fi_type, gve_type, mp_set_video_codec).
// Go lacks multi-char literal syntax, so the tags become typed constants.
type FourCC uint32

// Frame/codec type tags — C literals preserved via char shifts.
const (
	FourCCNone FourCC = 'n'<<24 | 'o'<<16 | 'n'<<8 | 'e' // C: 'none'
	FourCCLAVC FourCC = 'L'<<24 | 'A'<<16 | 'V'<<8 | 'C' // C: 'LAVC'
	FourCCYUVP FourCC = 'Y'<<24 | 'U'<<16 | 'V'<<8 | 'P' // C: 'YUVP'
	FourCCYUVp FourCC = 'Y'<<24 | 'U'<<16 | 'V'<<8 | 'p' // C: 'YUVp' (YUV420P10LE)
	FourCCVDPA FourCC = 'V'<<24 | 'D'<<16 | 'P'<<8 | 'A' // C: 'VDPA'
	FourCCVAAE FourCC = 'V'<<24 | 'A'<<16 | 'A'<<8 | 'E' // port extension (VAAPI)
	FourCCBGR  FourCC = 'B'<<16 | 'G'<<8 | 'R'           // C: 'BGR' (3-char literal)
	FourCCXYZ6 FourCC = 'X'<<24 | 'Y'<<16 | 'Z'<<8 | '6' // C: 'XYZ6'
	FourCCSURF FourCC = 'S'<<24 | 'U'<<16 | 'R'<<8 | 'F' // C: 'SURF'
	FourCCCVPB FourCC = 'C'<<24 | 'V'<<16 | 'P'<<8 | 'B' // C: 'CVPB'
	FourCCCEDR FourCC = 'C'<<24 | 'E'<<16 | 'D'<<8 | 'R' // C: 'CEDR'
	FourCCCED2 FourCC = 'C'<<24 | 'E'<<16 | 'D'<<8 | '2' // C: 'CED2'
	FourCCOMX  FourCC = 'o'<<16 | 'm'<<8 | 'x'           // C: 'omx' (3-char literal)
)

// String renders the tag in ASCII ("YUVP") for logs/debug output.
// Shorter tags ('BGR', 'omx') carry leading NUL bytes which are skipped.
func (f FourCC) String() string {
	b := [4]byte{byte(f >> 24), byte(f >> 16), byte(f >> 8), byte(f)}
	i := 0
	for i < 3 && b[i] == 0 {
		i++
	}
	return string(b[i:])
}
