/*
 *  Copyright (C) 2007-2015 Lonelycoder AB
 *  Canonical 1:1 Go port of src/backend/dvd/dvd.c's libdvdnav surface.
 *
 *  The bundled ext/dvd C sources (libdvdcss+libdvdread+libdvdnav, the
 *  .c files in this directory) run inside the wasm2go module
 *  (internal/dvdwasm) — no cgo. All handles are wasm32 addresses into
 *  the module's linear memory; pci_t travels as an opaque byte blob
 *  (wasm32 layout) exactly like the C memcpy of pci_t.
 */

package dvdlib

import (
	"encoding/binary"
	"sync"

	"github.com/czz/movian-go/internal/dvdwasm"
)

var (
	dvwMod  *dvdwasm.Module
	dvwOnce sync.Once
	pciBuf  int32 // wasm-malloc'd scratch holding one pci_t
	pciSz   int32
)

func mod() *dvdwasm.Module {
	dvwOnce.Do(func() {
		dvwMod = dvdwasm.NewModule(faHost{})
		pciSz = dvwMod.Xdvw_pci_size()
		pciBuf = dvwMod.Xmalloc(pciSz)
	})
	return dvwMod
}

// PCI — C: pci_t (presentation control information block).
// The blob holds the wasm32 memory image of a pci_t: same bytes the
// C code memcpy()s into media buffers / event payloads.
type PCI struct {
	data []byte
}

// blob returns the backing bytes, grown to sizeof(pci_t) on demand.
func (p *PCI) blob() []byte {
	mod()
	if len(p.data) != int(pciSz) {
		d := make([]byte, pciSz)
		copy(d, p.data)
		p.data = d
	}
	return p.data
}

// toWasm copies the blob into the module's PCI scratch buffer and
// returns its wasm address.
func (p *PCI) toWasm() int32 {
	copy(dvdwasm.Mem(mod())[pciBuf:], p.blob())
	return pciBuf
}

// PCIBytes — sizeof(pci_t) raw bytes, as C's memcpy of pci_t.
func (p *PCI) PCIBytes() []byte {
	out := make([]byte, len(p.blob()))
	copy(out, p.data)
	return out
}

// PCIFromBytes — C: memcpy(&pci, payload, sizeof(pci_t))
func PCIFromBytes(b []byte) *PCI {
	p := &PCI{}
	d := p.blob()
	n := len(d)
	if len(b) < n {
		n = len(b)
	}
	copy(d, b[:n])
	return p
}

func pciI32(m *dvdwasm.Module, off int32) int32 {
	return int32(binary.LittleEndian.Uint32(dvdwasm.Mem(m)[m.Xdvw_scratch()+off:]))
}

// HliSS — C: pci.hli.hl_gi.hli_ss (dvd_in_menu test)
func (p *PCI) HliSS() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	return int(m.Xdvw_pci_hli_ss(p.toWasm()))
}

// BtnNs — C: pci.hli.hl_gi.btn_ns (glw_video_overlay.c button loop bound)
func (p *PCI) BtnNs() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	return int(m.Xdvw_pci_btn_ns(p.toWasm()))
}

// Btni — C: pci.hli.btnit[i] coordinates.
func (p *PCI) Btni(i int) (xStart, xEnd, yStart, yEnd int32) {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	m.Xdvw_pci_btni(p.toWasm(), int32(i))
	return pciI32(m, 0), pciI32(m, 4), pciI32(m, 8), pciI32(m, 12)
}

// HighlightArea — C: dvdnav_highlight_area_t (dvd_types.h:60-68)
type HighlightArea struct {
	Palette uint32
	SX      uint16
	SY      uint16
	EX      uint16
	EY      uint16
	PTS     uint32
	ButtonN uint32
}

// GetHighlightArea — C: dvdnav_get_highlight_area (highlight.c:294)
func (p *PCI) GetHighlightArea(button, mode int) (*HighlightArea, int) {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	r := m.Xdvw_pci_highlight(p.toWasm(), int32(button), int32(mode))
	s := dvdwasm.Mem(m)[m.Xdvw_scratch():]
	xy1 := binary.LittleEndian.Uint32(s[4:])
	xy2 := binary.LittleEndian.Uint32(s[8:])
	return &HighlightArea{
		Palette: binary.LittleEndian.Uint32(s[0:]),
		SX:      uint16(xy1),
		SY:      uint16(xy1 >> 16),
		EX:      uint16(xy2),
		EY:      uint16(xy2 >> 16),
		PTS:     binary.LittleEndian.Uint32(s[12:]),
		ButtonN: binary.LittleEndian.Uint32(s[16:]),
	}, int(r)
}

// VobuSPtm — C: pci.pci_gi.vobu_s_ptm
func (p *PCI) VobuSPtm() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	return int(m.Xdvw_pci_vobu_sptm(p.toWasm()))
}

// VobuEPtm — C: pci.pci_gi.vobu_e_ptm
func (p *PCI) VobuEPtm() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	return int(m.Xdvw_pci_vobu_eptm(p.toWasm()))
}

// Dvdnav — C: dvdnav_t *
type Dvdnav struct {
	dvd     int32
	lastBuf int32 // wasm ptr of the last block handed out
}

// DVDNAV_* event / status codes (dvdnav_events.h, dvdnav.h)
const (
	StatusOk  = 1 // DVDNAV_STATUS_OK
	StatusErr = 0 // DVDNAV_STATUS_ERR

	BlockOK           = 0  // DVDNAV_BLOCK_OK
	Nop               = 1  // DVDNAV_NOP
	StillFrame        = 2  // DVDNAV_STILL_FRAME
	SpuStreamChange   = 3  // DVDNAV_SPU_STREAM_CHANGE
	AudioStreamChange = 4  // DVDNAV_AUDIO_STREAM_CHANGE
	VtsChange         = 5  // DVDNAV_VTS_CHANGE
	CellChange        = 6  // DVDNAV_CELL_CHANGE
	NavPacket         = 7  // DVDNAV_NAV_PACKET
	Stop              = 8  // DVDNAV_STOP
	Highlight         = 9  // DVDNAV_HIGHLIGHT
	SpuClutChange     = 10 // DVDNAV_SPU_CLUT_CHANGE
	HopChannel        = 12 // DVDNAV_HOP_CHANNEL
	Wait              = 13 // DVDNAV_WAIT

	// DVD_AUDIO_FORMAT_* (dvd_types.h)
	DvdAudioFormatAC3      = 0 // DVD_AUDIO_FORMAT_AC3
	DvdAudioFormatMPEG     = 2 // DVD_AUDIO_FORMAT_MPEG
	DvdAudioFormatMPEG2Ext = 3 // DVD_AUDIO_FORMAT_MPEG2_EXT
	DvdAudioFormatLPCM     = 4 // DVD_AUDIO_FORMAT_LPCM
	DvdAudioFormatDTS      = 6 // DVD_AUDIO_FORMAT_DTS
	DvdAudioFormatSDDS     = 7 // DVD_AUDIO_FORMAT_SDDS

	// DVD_VIDEO_LB_LEN (dvdnav_internal.h)
	DvdVideoLbLen = 2048
)

// DvdnavOpen — C: dvdnav_open(&d, path, vfs ? &faops : NULL)
func DvdnavOpen(path string, vfs bool) (*Dvdnav, int) {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	n := int32(len(path) + 1)
	p := m.Xmalloc(n)
	copy(dvdwasm.Mem(m)[p:], path)
	dvdwasm.Mem(m)[p+int32(len(path))] = 0
	v := int32(0)
	if vfs {
		v = 1
	}
	r := m.Xdvw_open(p, v)
	m.Xfree(p)
	st := int(uint32(r >> 32))
	if st != StatusOk {
		return nil, st
	}
	return &Dvdnav{dvd: int32(uint32(r))}, st
}

// DvdnavClose — C: dvdnav_close
func (d *Dvdnav) DvdnavClose() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_close(d.dvd))
}

// GetNextCacheBlock — C: dvdnav_get_next_cache_block.
// The returned slice aliases the lib's buffer; valid until
// FreeCacheBlock (mirrors the C contract — C: buf is lib-owned).
func (d *Dvdnav) GetNextCacheBlock() ([]byte, int, int, int) {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	st := int(m.Xdvw_gncb(d.dvd))
	s := dvdwasm.Mem(m)[m.Xdvw_scratch():]
	buf := int32(binary.LittleEndian.Uint32(s[0:]))
	ev := int(int32(binary.LittleEndian.Uint32(s[4:])))
	ln := int(int32(binary.LittleEndian.Uint32(s[8:])))
	d.lastBuf = buf
	if buf == 0 || ln <= 0 {
		return nil, ev, ln, st
	}
	// Copy: cache-block consumers keep the bytes past later module
	// calls (and Mem may re-slice on grow), so return owned data.
	out := make([]byte, ln)
	copy(out, dvdwasm.Mem(m)[buf:buf+int32(ln)])
	return out, ev, ln, st
}

// FreeCacheBlock — C: dvdnav_free_cache_block
func (d *Dvdnav) FreeCacheBlock(buf []byte) {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	mod().Xdvw_free_cache_block(d.dvd, d.lastBuf)
	d.lastBuf = 0
}

// CurrentTitleInfo — C: dvdnav_current_title_info. Returns (title, part, status)
func (d *Dvdnav) CurrentTitleInfo() (int, int, int) {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	st := int(m.Xdvw_current_title_info(d.dvd))
	return int(pciI32(m, 0)), int(pciI32(m, 4)), st
}

// DescribeTitleChapters — C: dvdnav_describe_title_chapters.
// Returns (numParts, chapterTimes, duration).
func (d *Dvdnav) DescribeTitleChapters(title int) (int, []int64, int64) {
	parts := d.GetNumberOfParts(title)
	if parts <= 0 {
		return parts, nil, 0
	}
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	tp := m.Xmalloc(int32(parts * 8))
	r := int(m.Xdvw_describe_chapters(d.dvd, int32(title), tp))
	var times []int64
	if r == StatusOk { // C: r != StatusOk → nil times (wrap returned parts)
		s := dvdwasm.Mem(m)
		times = make([]int64, parts)
		for i := range parts {
			times[i] = int64(binary.LittleEndian.Uint64(
				s[tp+int32(i*8):]))
		}
	}
	m.Xfree(tp)
	dur := int64(binary.LittleEndian.Uint64(
		dvdwasm.Mem(m)[m.Xdvw_scratch():]))
	return parts, times, dur
}

// GetNumberOfParts — C: dvdnav_get_number_of_parts
func (d *Dvdnav) GetNumberOfParts(title int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	if int(m.Xdvw_num_parts(d.dvd, int32(title))) != StatusOk {
		return 0
	}
	return int(pciI32(m, 0))
}

// GetNumberOfTitles — C: dvdnav_get_number_of_titles
func (d *Dvdnav) GetNumberOfTitles() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	if int(m.Xdvw_num_titles(d.dvd)) != StatusOk {
		return 0
	}
	return int(pciI32(m, 0))
}

// GetTitleString — C: dvdnav_get_title_string. Returns (title, status)
func (d *Dvdnav) GetTitleString() (string, int) {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	st := int(m.Xdvw_title_string(d.dvd))
	p := binary.LittleEndian.Uint32(dvdwasm.Mem(m)[m.Xdvw_scratch():])
	if p == 0 {
		return "", st
	}
	mem := dvdwasm.Mem(m)
	end := p
	for end < uint32(len(mem)) && mem[end] != 0 {
		end++
	}
	return string(mem[p:end]), st
}

// GetCurrentTime — C: dvdnav_get_current_time (90 kHz units)
func (d *Dvdnav) GetCurrentTime() int64 {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return mod().Xdvw_current_time(d.dvd)
}

// GetCurrentNavPCI — C: dvdnav_get_current_nav_pci (copy, like dvd.c's memcpy)
func (d *Dvdnav) GetCurrentNavPCI() *PCI {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	src := m.Xdvw_nav_pci(d.dvd)
	if src == 0 {
		return nil
	}
	p := &PCI{}
	copy(p.blob(), dvdwasm.Mem(m)[src:src+pciSz])
	return p
}

// GetVideoAspect — C: dvdnav_get_video_aspect
func (d *Dvdnav) GetVideoAspect() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_video_aspect(d.dvd))
}

// GetVideoResolution — C: dvdnav_get_video_resolution
func (d *Dvdnav) GetVideoResolution() (int, int) {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	m.Xdvw_video_resolution(d.dvd)
	return int(pciI32(m, 0)), int(pciI32(m, 4))
}

// GetAudioLogicalStream — C: dvdnav_get_audio_logical_stream
func (d *Dvdnav) GetAudioLogicalStream(idx int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_audio_logical(d.dvd, int32(idx)))
}

// AudioStreamFormat — C: dvdnav_audio_stream_format
func (d *Dvdnav) AudioStreamFormat(idx int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_audio_format(d.dvd, int32(idx)))
}

// AudioStreamChannels — C: dvdnav_audio_stream_channels
func (d *Dvdnav) AudioStreamChannels(idx int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_audio_channels(d.dvd, int32(idx)))
}

// AudioStreamToLang — C: dvdnav_audio_stream_to_lang (0xffff = none)
func (d *Dvdnav) AudioStreamToLang(idx int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_audio_lang(d.dvd, int32(idx)))
}

// GetSpuLogicalStream — C: dvdnav_get_spu_logical_stream
func (d *Dvdnav) GetSpuLogicalStream(idx int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_spu_logical(d.dvd, int32(idx)))
}

// SpuStreamToLang — C: dvdnav_spu_stream_to_lang (0xffff = none)
func (d *Dvdnav) SpuStreamToLang(idx int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_spu_lang(d.dvd, int32(idx)))
}

// ButtonActivate — C: dvdnav_button_activate
func (d *Dvdnav) ButtonActivate(pci *PCI) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_btn_activate(d.dvd, pci.toWasm()))
}

// ButtonSelect — C: dvdnav_button_select
func (d *Dvdnav) ButtonSelect(pci *PCI, btn int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_btn_select(d.dvd, pci.toWasm(), int32(btn)))
}

// ButtonSelectAndActivate — C: dvdnav_button_select_and_activate
func (d *Dvdnav) ButtonSelectAndActivate(pci *PCI, btn int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_btn_select_activate(d.dvd, pci.toWasm(),
		int32(btn)))
}

// UpperButtonSelect — C: dvdnav_upper_button_select
func (d *Dvdnav) UpperButtonSelect(pci *PCI) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_btn_upper(d.dvd, pci.toWasm()))
}

// LowerButtonSelect — C: dvdnav_lower_button_select
func (d *Dvdnav) LowerButtonSelect(pci *PCI) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_btn_lower(d.dvd, pci.toWasm()))
}

// LeftButtonSelect — C: dvdnav_left_button_select
func (d *Dvdnav) LeftButtonSelect(pci *PCI) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_btn_left(d.dvd, pci.toWasm()))
}

// RightButtonSelect — C: dvdnav_right_button_select
func (d *Dvdnav) RightButtonSelect(pci *PCI) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_btn_right(d.dvd, pci.toWasm()))
}

// StillSkip — C: dvdnav_still_skip
func (d *Dvdnav) StillSkip() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_still_skip(d.dvd))
}

// WaitSkip — C: dvdnav_wait_skip
func (d *Dvdnav) WaitSkip() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_wait_skip(d.dvd))
}

// NextPgSearch — C: dvdnav_next_pg_search
func (d *Dvdnav) NextPgSearch() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_next_pg(d.dvd))
}

// PrevPgSearch — C: dvdnav_prev_pg_search
func (d *Dvdnav) PrevPgSearch() int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_prev_pg(d.dvd))
}

// SetReadaheadFlag — C: dvdnav_set_readahead_flag
func (d *Dvdnav) SetReadaheadFlag(flag int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_set_readahead(d.dvd, int32(flag)))
}

// SetPGCPositioningFlag — C: dvdnav_set_PGC_positioning_flag
func (d *Dvdnav) SetPGCPositioningFlag(flag int) int {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	return int(mod().Xdvw_set_pgc_pos(d.dvd, int32(flag)))
}

// ErrString — C: dvdnav_err_to_string
func (d *Dvdnav) ErrString() string {
	dvdwasm.Mu.Lock()
	defer dvdwasm.Mu.Unlock()
	m := mod()
	p := m.Xdvw_errstr(d.dvd)
	mem := dvdwasm.Mem(m)
	end := p
	for end < int32(len(mem)) && mem[end] != 0 {
		end++
	}
	return string(mem[p:end])
}
