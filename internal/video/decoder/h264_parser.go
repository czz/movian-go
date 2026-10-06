package decoder

// Canonical port of src/video/h264_parser.c + h264_parser.h.
//
// Slice-header / SPS / PPS parsing and POC computation over
// misc.BitstreamT (C: misc/bitstream.h function-pointer vtable).

import (
	miscpkg "github.com/czz/movian-go/internal/misc"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: h264_parser.h:27-31 — slice types.
const (
	SliceTypeP  = 0
	SliceTypeB  = 1
	SliceTypeI  = 2
	SliceTypeSP = 3
	SliceTypeSI = 4
)

// C: h264_parser.h:36-37 — parser table bounds.
const (
	H264ParserNumSPS = 4
	H264ParserNumPPS = 16
)

// C: h264_parser.h:39-47 — MMCO opcodes.
const (
	H264MMCOSize         = 66
	H264MMCOEnd          = 0
	H264MMCOShort2Unused = 1
	H264MMCOLong2Unused  = 2
	H264MMCOShort2Long   = 3
	H264MMCOSetMaxLong   = 4
	H264MMCOReset        = 5
	H264MMCOLong         = 6
)

// H264SPS — C: h264_sps_t (h264_parser.h:53-93).
type H264SPS struct {
	MBWidth   uint16
	MBHeight  uint16
	AspectNum uint16
	AspectDen uint16

	NumRefFrames uint8

	MBSOnlyFlag                    uint8
	AFF                            uint8
	FixedRate                      uint8
	GapsInFrameNumValueAllowedFlag bool

	Profile uint8
	Level   uint8

	ChromaFormat               uint8
	BitDepthLuma               uint8
	BitDepthChroma             uint8
	ResidualColorTransformFlag bool
	TransformBypass            uint8

	MaxFrameNumBits uint8
	POCType         uint

	Log2MaxPOCLsb int

	DeltaPicOrderAlwaysZeroFlag bool
	OffsetForNonRefPic          int
	OffsetForTopToBottomField   int
	POCCycleLength              int
	Direct8x8InferenceFlag      bool

	CropLeft   uint16
	CropRight  uint16
	CropTop    uint16
	CropBottom uint16

	Present bool // C: char present
}

// H264PPS — C: h264_pps_t (h264_parser.h:99-116).
type H264PPS struct {
	SPSID                             int
	RefCount                          [2]uint
	CABAC                             bool // C: char cabac
	PicOrderPresent                   bool
	WeightedPredFlag                  bool
	WeightedBipredIDC                 uint8
	DeblockingFilterParametersPresent bool
	ConstrainedIntraPred              bool
	RedundantPicCntPresent            bool
	Transform8x8Mode                  bool
	Present                           bool

	InitQP              int
	InitQS              int
	ChromaQPIndexOffset [2]int
	SliceGroupCount     int
}

// H264Frame — C: h264_frame_t (h264_parser.h:122-136).
type H264Frame struct {
	Picture      any // C: void *picture
	POC          int
	FrameIdx     uint16
	IsReference  uint8
	OutputNeeded uint8
	IsLongTerm   uint8
	MetaIndex    uint8
	Pos          int
}

// H264ParserOutputFrame — C: h264_parser_output_frame_t.
type H264ParserOutputFrame func(opaque any, frame *H264Frame)

// H264ParserPictureRelease — C: h264_parser_picture_release_t.
type H264ParserPictureRelease func(picture any)

// H264Parser — C: h264_parser_t (h264_parser.h:144-196).
type H264Parser struct {
	SPSArray [H264ParserNumSPS]H264SPS
	PPSArray [H264ParserNumPPS]H264PPS

	SPS *H264SPS // C: const h264_sps_t *sps
	PPS *H264PPS // C: const h264_pps_t *pps

	Lensize int // C: lensize — size of NAL length

	RefCount int

	NALRefIDC   int
	NALUnitType int

	CurrentFrame H264Frame

	FirstMbInSlice int
	SliceType      int
	SliceTypeNOS   int
	FrameNum       int
	FrameNumOffset int
	PrevFrameNum   int

	FieldPicFlag    bool
	BottomFieldFlag bool

	IDRPicID int

	PicOrderCntLsb         int
	DeltaPicOrderCntBottom int

	DeltaPicOrderCnt [2]int

	RedundantPicCnt int

	DirectSpatialMvPredFlag bool

	NumRefIdxActiveOverrideFlag bool

	NumRefIdxL0ActiveMinus1 int
	NumRefIdxL1ActiveMinus1 int

	PrevPOCLsb int
	POCMsb     int

	MaxFrameNum int
}

// decodeScalingList — C: decode_scaling_list (h264_parser.c:34-48).
func decodeScalingList(bs *miscpkg.BitstreamT, size int) {
	last, next := 8, 8
	if bs.ReadBits1(bs) == 0 {
		return
	}

	for i := range size {
		if next != 0 {
			next = (last + bs.ReadGolombSe(bs)) & 0xff
		}
		if i == 0 && next == 0 {
			break
		}
		if next != 0 {
			last = next
		}
	}
}

// DecodeSPS — C: h264_parser_decode_sps (h264_parser.c:54-147).
// C: when hp == NULL, sps must be non-NULL (assert) and the id is only
// returned; the caller's sps is filled in place.
func (hp *H264Parser) DecodeSPS(bs *miscpkg.BitstreamT, sps *H264SPS) int {
	profile := int(bs.ReadBits(bs, 8))
	bs.SkipBits(bs, 8)
	level := int(bs.ReadBits(bs, 8))
	spsID := bs.ReadGolombUe(bs)

	if hp == nil {
		// C: assert(sps != NULL)
		if sps == nil {
			panic("h264_parser_decode_sps: sps == nil with hp == nil")
		}
	} else {
		if spsID >= H264ParserNumSPS {
			return -1
		}
		sps = &hp.SPSArray[spsID]
	}

	sps.Present = true
	sps.Profile = uint8(profile)
	sps.Level = uint8(level)

	if sps.Profile == 100 || sps.Profile == 110 ||
		sps.Profile == 122 || sps.Profile == 244 ||
		sps.Profile == 44 || sps.Profile == 83 ||
		sps.Profile == 86 || sps.Profile == 118 ||
		sps.Profile == 128 || sps.Profile == 144 {

		sps.ChromaFormat = uint8(bs.ReadGolombUe(bs))
		if sps.ChromaFormat == 3 {
			sps.ResidualColorTransformFlag = bs.ReadBits(bs, 1) != 0
		}

		sps.BitDepthLuma = uint8(bs.ReadGolombUe(bs) + 8)
		sps.BitDepthChroma = uint8(bs.ReadGolombUe(bs) + 8)
		sps.TransformBypass = uint8(bs.ReadBits(bs, 1))

		if bs.ReadBits1(bs) != 0 {
			decodeScalingList(bs, 16)
			decodeScalingList(bs, 16)
			decodeScalingList(bs, 16)
			decodeScalingList(bs, 16)
			decodeScalingList(bs, 16)
			decodeScalingList(bs, 16)
			decodeScalingList(bs, 64)
			decodeScalingList(bs, 64)
		}
	} else {
		sps.ChromaFormat = 1
		sps.BitDepthLuma = 8
		sps.BitDepthChroma = 8
	}

	sps.MaxFrameNumBits = uint8(bs.ReadGolombUe(bs) + 4)
	sps.POCType = bs.ReadGolombUe(bs)
	if sps.POCType == 0 {
		sps.Log2MaxPOCLsb = int(bs.ReadGolombUe(bs)) + 4
	} else if sps.POCType == 1 {
		sps.DeltaPicOrderAlwaysZeroFlag = bs.ReadBits1(bs) != 0
		sps.OffsetForNonRefPic = bs.ReadGolombSe(bs)
		sps.OffsetForTopToBottomField = bs.ReadGolombSe(bs)
		sps.POCCycleLength = int(bs.ReadGolombUe(bs))
		for range sps.POCCycleLength {
			bs.ReadGolombSe(bs)
		}
	} else if sps.POCType != 2 {
		return -1
	}

	sps.NumRefFrames = uint8(bs.ReadGolombUe(bs))

	sps.GapsInFrameNumValueAllowedFlag = bs.ReadBits1(bs) != 0

	sps.MBWidth = uint16(bs.ReadGolombUe(bs) + 1)
	sps.MBHeight = uint16(bs.ReadGolombUe(bs) + 1)
	sps.MBSOnlyFlag = uint8(bs.ReadBits1(bs))
	if sps.MBSOnlyFlag == 0 {
		sps.AFF = uint8(bs.ReadBits1(bs))
	}

	sps.Direct8x8InferenceFlag = bs.ReadBits1(bs) != 0

	if bs.ReadBits1(bs) != 0 {
		// C: const int hshift = chroma_format == 1 || == 2; vshift = == 1
		hshift := 0
		if sps.ChromaFormat == 1 || sps.ChromaFormat == 2 {
			hshift = 1
		}
		vshift := 0
		if sps.ChromaFormat == 1 {
			vshift = 1
		}

		hscale := uint16(1 << hshift)
		vscale := uint16((2 - int(sps.MBSOnlyFlag)) << vshift)

		sps.CropLeft = uint16(bs.ReadGolombUe(bs)) * hscale
		sps.CropRight = uint16(bs.ReadGolombUe(bs)) * hscale
		sps.CropTop = uint16(bs.ReadGolombUe(bs)) * vscale
		sps.CropBottom = uint16(bs.ReadGolombUe(bs)) * vscale
	}
	return int(spsID)
}

// DecodePPS — C: h264_parser_decode_pps (h264_parser.c:153-190).
func (hp *H264Parser) DecodePPS(bs *miscpkg.BitstreamT) {
	ppsID := bs.ReadGolombUe(bs)
	if ppsID >= H264ParserNumPPS {
		return
	}

	spsID := bs.ReadGolombUe(bs)
	if spsID >= H264ParserNumSPS {
		return
	}

	pps := &hp.PPSArray[ppsID]

	pps.Present = true

	pps.SPSID = int(spsID)
	pps.CABAC = bs.ReadBits1(bs) != 0
	pps.PicOrderPresent = bs.ReadBits1(bs) != 0
	pps.SliceGroupCount = int(bs.ReadGolombUe(bs)) + 1
	pps.RefCount[0] = bs.ReadGolombUe(bs) + 1
	pps.RefCount[1] = bs.ReadGolombUe(bs) + 1
	pps.WeightedPredFlag = bs.ReadBits1(bs) != 0
	pps.WeightedBipredIDC = uint8(bs.ReadBits(bs, 2))
	pps.InitQP = bs.ReadGolombSe(bs) + 26
	pps.InitQS = bs.ReadGolombSe(bs) + 26
	pps.ChromaQPIndexOffset[0] = bs.ReadGolombSe(bs)
	pps.DeblockingFilterParametersPresent = bs.ReadBits1(bs) != 0
	pps.ConstrainedIntraPred = bs.ReadBits1(bs) != 0
	pps.RedundantPicCntPresent = bs.ReadBits1(bs) != 0
	pps.Transform8x8Mode = false

	pps.ChromaQPIndexOffset[1] = pps.ChromaQPIndexOffset[0]

	bitsLeft := bs.BitsLeftField(bs)
	if bitsLeft <= 7 {
		return
	}
	pps.Transform8x8Mode = bs.ReadBits1(bs) != 0
}

// Start — C: h264_parser_init (h264_parser.c:196-251).
func (hp *H264Parser) Setup(data []byte) int {
	var bs miscpkg.BitstreamT

	// C: memset(hp, 0, sizeof(h264_parser_t))
	*hp = H264Parser{}

	if data == nil {
		return 0
	}

	if len(data) < 7 {
		return -1
	}

	if data[0] != 1 {
		hp.DecodeData(data)
		return 0
	}

	hp.Lensize = int(data[4]&0x3) + 1

	n := int(data[5] & 0x1f)
	data = data[6:]

	// Parse SPS
	for i := 0; i < n && len(data) >= 2; i++ {
		s := int(data[0])<<8 | int(data[1])
		s += 2
		if len(data) < s {
			break
		}

		// C: init_rbits(&bs, data+3, s-3, 0)
		miscpkg.SetupRbits(&bs, data[min(3, len(data)):], s-3, 0)
		hp.DecodeSPS(&bs, nil)
		data = data[s:]
	}

	// Parse PPS
	if len(data) < 1 {
		return -1
	}
	n = int(data[0])
	data = data[1:]

	for i := 0; i < n && len(data) >= 2; i++ {
		s := int(data[0])<<8 | int(data[1])
		s += 2
		if len(data) < s {
			break
		}

		// C: init_rbits(&bs, data+3, s-3, 0)
		miscpkg.SetupRbits(&bs, data[min(3, len(data)):], s-3, 0)
		hp.DecodePPS(&bs)
		data = data[s:]
	}
	return 0
}

// calcPOC — C: calc_poc (h264_parser.c:258-294).
func calcPOC(hp *H264Parser, sps *H264SPS) int {
	maxFrameNum := 1 << sps.MaxFrameNumBits
	poc := 0

	if hp.FrameNum < hp.PrevFrameNum {
		hp.FrameNumOffset += maxFrameNum
	}

	if sps.POCType == 0 {
		maxPOCCntLsb := uint32(1 << uint(sps.Log2MaxPOCLsb))

		if hp.PicOrderCntLsb < hp.PrevPOCLsb &&
			hp.PrevPOCLsb-hp.PicOrderCntLsb >= int(maxPOCCntLsb/2) {

			hp.POCMsb += int(maxPOCCntLsb)
		} else if hp.PicOrderCntLsb > hp.PrevPOCLsb &&
			hp.PicOrderCntLsb-hp.PrevPOCLsb > int(maxPOCCntLsb/2) {
			hp.POCMsb -= int(maxPOCCntLsb)
		}

		poc = hp.POCMsb + hp.PicOrderCntLsb

		hp.PrevPOCLsb = hp.PicOrderCntLsb
	} else if sps.POCType == 2 {
		poc = 2 * (hp.FrameNumOffset + hp.FrameNum)
		if hp.NALRefIDC == 0 {
			poc--
		}
	} else {
		// C: empty else branch — poc stays 0
	}
	return poc
}

// DecodeSliceHeader — C: h264_parser_decode_slice_header
// (h264_parser.c:300-375).
func (hp *H264Parser) DecodeSliceHeader(bs *miscpkg.BitstreamT) {
	hp.FirstMbInSlice = int(bs.ReadGolombUe(bs))
	hp.SliceType = int(bs.ReadGolombUe(bs))
	if hp.SliceType >= 5 {
		hp.SliceType -= 5
	}

	hp.SliceTypeNOS = hp.SliceType & 3

	ppsID := bs.ReadGolombUe(bs)

	if ppsID >= H264ParserNumPPS {
		return
	}

	pps := &hp.PPSArray[ppsID]
	sps := &hp.SPSArray[pps.SPSID]

	hp.SPS = sps
	hp.PPS = pps

	hp.FrameNum = int(bs.ReadBits(bs, int(sps.MaxFrameNumBits)))
	hp.MaxFrameNum = 1 << sps.MaxFrameNumBits

	if sps.MBSOnlyFlag == 0 {
		hp.FieldPicFlag = bs.ReadBits(bs, 1) != 0
		if hp.FieldPicFlag {
			hp.BottomFieldFlag = bs.ReadBits(bs, 1) != 0
		}
	}

	if hp.NALUnitType == 5 {
		hp.IDRPicID = int(bs.ReadGolombUe(bs))
	}

	if sps.POCType == 0 {
		hp.PicOrderCntLsb = int(bs.ReadBits(bs, sps.Log2MaxPOCLsb))
		if pps.PicOrderPresent && !hp.FieldPicFlag {
			hp.DeltaPicOrderCntBottom = bs.ReadGolombSe(bs)
		}
	}

	if sps.POCType == 1 && !sps.DeltaPicOrderAlwaysZeroFlag {
		hp.DeltaPicOrderCnt[0] = bs.ReadGolombSe(bs)
		if pps.PicOrderPresent && !hp.FieldPicFlag {
			hp.DeltaPicOrderCnt[1] = bs.ReadGolombSe(bs)
		}
	}

	if pps.RedundantPicCntPresent {
		hp.RedundantPicCnt = int(bs.ReadGolombUe(bs))
	}

	hp.NumRefIdxL0ActiveMinus1 = int(pps.RefCount[0]) - 1
	hp.NumRefIdxL1ActiveMinus1 = int(pps.RefCount[1]) - 1

	if hp.SliceTypeNOS == SliceTypeB {
		hp.DirectSpatialMvPredFlag = bs.ReadBits(bs, 1) != 0
	} else {
		hp.DirectSpatialMvPredFlag = false
	}

	if hp.SliceTypeNOS == SliceTypeP || hp.SliceTypeNOS == SliceTypeB {
		hp.NumRefIdxActiveOverrideFlag = bs.ReadBits(bs, 1) != 0
		if hp.NumRefIdxActiveOverrideFlag {
			hp.NumRefIdxL0ActiveMinus1 = int(bs.ReadGolombUe(bs))
			if hp.SliceType == SliceTypeB {
				hp.NumRefIdxL1ActiveMinus1 = int(bs.ReadGolombUe(bs))
			}
		}
	}

	hp.CurrentFrame.POC = calcPOC(hp, sps)
}

// idr — C: idr (h264_parser.c:381-387).
func idr(hp *H264Parser) {
	hp.PrevPOCLsb = 0
	hp.FrameNumOffset = 0
	hp.PrevFrameNum = 0
}

// DecodeNALFromBS — C: h264_parser_decode_nal_from_bs
// (h264_parser.c:392-408). C: case 5 falls through to case 1 (no break).
func (hp *H264Parser) DecodeNALFromBS(bs *miscpkg.BitstreamT) {
	switch hp.NALUnitType {
	case 5: // IDR — C: falls through into case 1 (no break)
		idr(hp)
		fallthrough // C: no break between case 5 and case 1
	case 1: // Slice
		hp.DecodeSliceHeader(bs)
	case 7: // SPS
		hp.DecodeSPS(bs, nil)
	case 8: // PPS
		hp.DecodePPS(bs)
	}
}

// DecodeNAL — C: h264_parser_decode_nal (h264_parser.c:415-430).
func (hp *H264Parser) DecodeNAL(data []byte) {
	if len(data) == 0 {
		return
	}

	hp.NALRefIDC = int(data[0]) >> 5
	hp.NALUnitType = int(data[0]) & 0x1f
	data = data[1:]

	var bs miscpkg.BitstreamT
	miscpkg.SetupRbits(&bs, data, len(data), 1)
	hp.DecodeNALFromBS(&bs)
}

// Fini — C: h264_parser_fini (h264_parser.c:436-439) — no-op.
func (hp *H264Parser) Fini() {
}

// DecodeData — C: h264_parser_decode_data (h264_parser.c:445-486).
// C quirk preserved: the length-prefixed path passes the WHOLE remaining
// buffer length (not nal_len) to h264_parser_decode_nal.
func (hp *H264Parser) DecodeData(d []byte) {
	if hp.Lensize == 0 {
		p := -1 // C: const uint8_t *p = NULL — index into d, -1 = NULL

		i := 0
		for len(d)-i > 3 {
			if !(d[i] == 0 && d[i+1] == 0 && d[i+2] == 1) {
				i++
				continue
			}

			if p >= 0 {
				hp.DecodeNAL(d[p:i])
			}

			i += 3
			p = i
		}
		i = len(d)

		if p >= 0 {
			hp.DecodeNAL(d[p:i])
		}
	} else {
		l := len(d)
		for l >= hp.Lensize {
			nalLen := 0
			for i := range hp.Lensize {
				nalLen = nalLen<<8 | int(d[i])
			}
			d = d[hp.Lensize:]
			l -= hp.Lensize

			// C: h264_parser_decode_nal(hp, d, len) — len is the
			// remaining buffer, NOT nal_len.
			hp.DecodeNAL(d[:l])

			// C: d += nal_len; len -= nal_len — when nal_len runs
			// past the buffer, len goes negative and the loop ends.
			adv := min(nalLen, len(d))
			d = d[adv:]
			l -= nalLen
		}
	}
}

// H264DumpExtradata — C: h264_dump_extradata (h264_parser.c:492-537).
func H264DumpExtradata(data []byte) {
	var hp H264Parser

	decoderTS.ts.HexDump("h264", data)

	if hp.Setup(data) != 0 {
		decoderTS.ts.Trace(tracepkg.TRACE_DEBUG, "h264", "Corrupt extradata")
		return
	}

	for i := range H264ParserNumSPS {
		s := &hp.SPSArray[i]
		if !s.Present {
			continue
		}
		decoderTS.ts.Trace(tracepkg.TRACE_DEBUG, "h264",
			"SPS[%d]: %d x %d profile:%d level:%d.%d ref-frames:%d",
			i, int(s.MBWidth)*16, int(s.MBHeight)*16,
			s.Profile,
			int(s.Level)/10,
			int(s.Level)%10,
			s.NumRefFrames)
		decoderTS.ts.Trace(tracepkg.TRACE_DEBUG, "h264",
			"        chromaformat:%d lumabits:%d chromabits:%d",
			s.ChromaFormat,
			s.BitDepthLuma,
			s.BitDepthChroma)
	}

	for i := range H264ParserNumPPS {
		p := &hp.PPSArray[i]
		if !p.Present {
			continue
		}
		cabac := "CAVLC"
		if p.CABAC {
			cabac = "CABAC"
		}
		decoderTS.ts.Trace(tracepkg.TRACE_DEBUG, "h264",
			"PPS[%d]: %s pop:%d wpred:%d wbipred:%d deblock:%d",
			i, cabac,
			miscpkg.BoolToInt(p.PicOrderPresent),
			miscpkg.BoolToInt(p.WeightedPredFlag),
			p.WeightedBipredIDC,
			miscpkg.BoolToInt(p.DeblockingFilterParametersPresent))
	}

	hp.Fini()
}
