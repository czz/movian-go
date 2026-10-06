//go:build rpi

// rpi_audio.go — C: src/arch/rpi/rpi_audio.c — OpenMAX IL audio driver
// (OMX.broadcom.audio_decode / audio_mixer / audio_render chain).
//
// The driver class is registered via audiocore.PlatformAudioDriverStartHook
// (audio/core/audio_rpi.go) — C resolves audio_driver_init at link time;
// Go needs the indirection because arch/rpi imports audio/core (for
// AudioDecoder/AudioClass) so audio/core cannot import arch/rpi.
package rpi

/*
#cgo CFLAGS: -DOMX_SKIP64BIT -I${SRCDIR}/../../../ffmpeg/rpi/include -I/opt/vc/include -I/opt/vc/include/interface/vmcs_host/linux -I/opt/vc/include/interface/vcos/pthreads
#cgo LDFLAGS: -L${SRCDIR}/../../../ffmpeg/rpi/lib -L/opt/vc/lib -lbcm_host -lvcos -lvchiq_arm -lopenmaxil -lEGL -lGLESv2 -lmmal_core -lmmal_util -lmmal_vc_client -lvchostif -lavcodec -lavformat -lavutil

#include <stdlib.h>
#include <string.h>
#include <OMX_Core.h>
#include <OMX_Component.h>
#include <OMX_Broadcom.h>
#include "omx_shim.h"
#include <interface/vmcs_host/vc_tvservice.h>
#include <libavcodec/avcodec.h>

static OMX_AUDIO_PORTDEFINITIONTYPE *pda(OMX_PARAM_PORTDEFINITIONTYPE *p) { return &p->format.audio; }
*/
import "C"

import (
	"encoding/binary"
	"math"
	"unsafe"

	"github.com/czz/movian-go/internal/arch"
	audiocore "github.com/czz/movian-go/internal/audio/core"
	mediacore "github.com/czz/movian-go/internal/media/core"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// C: omx_enable_vorbis / omx_enable_flac (rpi_audio.c:33-34)
var (
	omxEnableVorbis bool
	omxEnableFlac   bool
)

// C: decoder_t (rpi_audio.c:37-66) — the ac_alloc_size extension; the
// base audio_decoder_t is audiocore.AudioDecoder and this lives in
// ad.AudioInstance. C embeds ad as member 0 so decoder_t* == ad*; Go
// keeps an explicit back-pointer.
type rpiDecoder struct {
	ad *audiocore.AudioDecoder // C: embedded audio_decoder_t

	Decoder *OmxComponent // C: d_decoder
	Mixer   *OmxComponent // C: d_mixer
	Render  *OmxComponent // C: d_render

	MixerTun  *OmxTunnel // C: d_mixer_tun
	RenderTun *OmxTunnel // C: d_render_tun
	ClockTun  *OmxTunnel // C: d_clock_tun

	Bpf       int // C: d_bpf — bytes per frame
	LastEpoch int // C: d_last_epoch

	Channels int // C: d_channels

	Gain         float64 // C: d_gain
	MasterVolume float64 // C: d_master_volume
	MasterMute   float64 // C: d_master_mute

	Hdmi8chPcm bool // C: d_hdmi_8ch_pcm
	HdmiAc3    bool // C: d_hdmi_ac3
	HdmiEac3   bool // C: d_hdmi_eac3
	HdmiDts    bool // C: d_hdmi_dts
	HdmiMlp    bool // C: d_hdmi_mlp
	HdmiAac    bool // C: d_hdmi_aac

	LocalOutput bool // C: d_local_output

	Matrix [8][8]float64 // C: d_matrix
}

// C: WAVEFORMATEX / WAVEFORMATEXTENSIBLE / GUID (rpi_audio.c:69-100)
// packed little-endian, marshalled by marshalWFE below.
type waveFormatExtensible struct {
	formatTag          uint16
	channels           uint16
	samplesPerSec      uint32
	avgBytesPerSec     uint32
	blockAlign         uint16
	bitsPerSample      uint16
	cbSize             uint16
	validBitsPerSample uint16
	channelMask        uint32
	subFormatTag       uint16  // GUID.Data1 low16 = wFormatTag
	subFormat          [8]byte // rest of KSDATAFORMAT_SUBTYPE_PCM
}

// C: KSDATAFORMAT_SUBTYPE_PCM (rpi_audio.c:102-106)
var ksDataFormatSubTypePCM = [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}

// marshalWFE serialises WAVEFORMATEXTENSIBLE (packed, little-endian).
func marshalWFE(w *waveFormatExtensible) []byte {
	b := make([]byte, 40)
	binary.LittleEndian.PutUint16(b[0:], w.formatTag)
	binary.LittleEndian.PutUint16(b[2:], w.channels)
	binary.LittleEndian.PutUint32(b[4:], w.samplesPerSec)
	binary.LittleEndian.PutUint32(b[8:], w.avgBytesPerSec)
	binary.LittleEndian.PutUint16(b[12:], w.blockAlign)
	binary.LittleEndian.PutUint16(b[14:], w.bitsPerSample)
	binary.LittleEndian.PutUint16(b[16:], w.cbSize)
	binary.LittleEndian.PutUint16(b[18:], w.validBitsPerSample)
	binary.LittleEndian.PutUint32(b[20:], w.channelMask)
	// GUID: Data1 u32 (wFormatTag), Data2 u16, Data3 u16, Data4 [8]u8
	binary.LittleEndian.PutUint32(b[24:], uint32(w.subFormatTag))
	binary.LittleEndian.PutUint16(b[28:], 0)
	binary.LittleEndian.PutUint16(b[30:], 0x0010)
	copy(b[32:], w.subFormat[:])
	return b
}

// C: master_volume / master_mute / local_output / hdmi_*_mode
// (rpi_audio.c:108-113)
var (
	masterVolume = 1.0
	masterMute   int
	localOutput  int
	hdmiAc3Mode  int
	hdmiDtsMode  int
	hdmi8chMode  int
)

// updateAudioClock — C: update_audio_clock (rpi_audio.c:118-131)
func updateAudioClock(ad *audiocore.AudioDecoder, epoch int) {
	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)
	if mp == nil {
		return
	}
	c := OmxGetClock(mp)
	if c != nil {
		ts := OmxGetMediaTime(c)
		mp.ClockMutex.Lock()
		mp.AudioClockAvtime = arch.GetAvtime()
		mp.AudioClock = ts
		mp.AudioClockEpoch = int64(epoch)
		mp.ClockMutex.Unlock()
	}
}

// checkMode — C: check_mode (rpi_audio.c:136-148)
// configured: 0=autodetect, 1=off, 2=on → configured ? configured-1 : detected
func checkMode(detected, configured int) int {
	if configured != 0 {
		return configured - 1
	}
	return detected
}

// getDecoder — lazily attaches the driver state, like alsa's getALSA
// (C: the class' ac_alloc_size makes the extension part of ad).
func getDecoder(ad *audiocore.AudioDecoder) *rpiDecoder {
	if d, ok := ad.AudioInstance.(*rpiDecoder); ok {
		return d
	}
	d := &rpiDecoder{ad: ad}
	ad.AudioInstance = d
	return d
}

// rpiAudioStart — C: rpi_audio_init (rpi_audio.c:153-193)
func rpiAudioStart(ad *audiocore.AudioDecoder) error {
	d := getDecoder(ad)
	d.Gain = 1.0
	d.MasterVolume = masterVolume
	d.MasterMute = float64(masterMute)

	d.LocalOutput = localOutput != 0

	d.Hdmi8chPcm = C.vc_tv_hdmi_audio_supported(C.EDID_AudioFormat_ePCM, 8,
		C.EDID_AudioSampleRate_e48KHz, C.EDID_AudioSampleSize_16bit) == 0

	d.HdmiAc3 = C.vc_tv_hdmi_audio_supported(C.EDID_AudioFormat_eAC3, 2,
		C.EDID_AudioSampleRate_e44KHz, C.EDID_AudioSampleSize_16bit) == 0

	d.HdmiEac3 = C.vc_tv_hdmi_audio_supported(C.EDID_AudioFormat_eEAC3, 2,
		C.EDID_AudioSampleRate_e44KHz, C.EDID_AudioSampleSize_16bit) == 0

	d.HdmiDts = C.vc_tv_hdmi_audio_supported(C.EDID_AudioFormat_eDTS, 2,
		C.EDID_AudioSampleRate_e44KHz, C.EDID_AudioSampleSize_16bit) == 0

	d.HdmiMlp = C.vc_tv_hdmi_audio_supported(C.EDID_AudioFormat_eMLP, 2,
		C.EDID_AudioSampleRate_e44KHz, C.EDID_AudioSampleSize_16bit) == 0

	d.HdmiAac = C.vc_tv_hdmi_audio_supported(C.EDID_AudioFormat_eAAC, 2,
		C.EDID_AudioSampleRate_e44KHz, C.EDID_AudioSampleSize_16bit) == 0

	yesno := func(b bool) string {
		if b {
			return "YES"
		}
		return "NO"
	}
	rpiTS.Trace(trace.TRACE_DEBUG, "RPI", "Supported audio formats "+
		"AC3:%s EAC3:%s DTS:%s MLP:%s AAC:%s 8ChPCM:%s",
		yesno(d.HdmiAc3), yesno(d.HdmiEac3), yesno(d.HdmiDts),
		yesno(d.HdmiMlp), yesno(d.HdmiAac), yesno(d.Hdmi8chPcm))

	if !d.Hdmi8chPcm {
		ad.StereoDownmix = true
	}
	return nil
}

// rpiAudioFini — C: rpi_audio_fini (rpi_audio.c:198-236)
func rpiAudioFini(ad *audiocore.AudioDecoder) {
	d := getDecoder(ad)

	if d.Render == nil {
		return
	}

	if d.RenderTun != nil {
		OmxTunnelDestroy(d.RenderTun)
		d.RenderTun = nil
	}
	if d.MixerTun != nil {
		OmxTunnelDestroy(d.MixerTun)
		d.MixerTun = nil
	}
	if d.ClockTun != nil {
		OmxTunnelDestroy(d.ClockTun)
		d.ClockTun = nil
	}

	OmxSetState(d.Decoder, C.OMX_StateIdle)
	OmxWaitBuffers(d.Decoder)
	OmxReleaseBuffers(d.Decoder, 120)
	OmxSetState(d.Decoder, C.OMX_StateLoaded)
	OmxComponentDestroy(d.Decoder)

	if d.Mixer != nil {
		OmxSetState(d.Mixer, C.OMX_StateIdle)
		OmxSetState(d.Mixer, C.OMX_StateLoaded)
		OmxComponentDestroy(d.Mixer)
	}
	d.Mixer = nil

	OmxSetState(d.Render, C.OMX_StateIdle)
	OmxSetState(d.Render, C.OMX_StateLoaded)
	OmxComponentDestroy(d.Render)
}

// C: channel_swizzle (rpi_audio.c:241)
var channelSwizzle = [8]int{0, 1, 3, 2, 4, 5, 6, 7}

// setMixerMatrix — C: set_mixer_matrix (rpi_audio.c:243-283)
func setMixerMatrix(d *rpiDecoder) {
	if d.Mixer == nil {
		return
	}

	var mix C.OMX_CONFIG_BRCMAUDIODOWNMIXCOEFFICIENTS8x8
	mix.nSize = C.OMX_U32(unsafe.Sizeof(mix))
	C.omx_init_version_(&mix.nVersion)

	gain := d.Gain * d.MasterVolume
	if d.MasterMute != 0 {
		gain = 0
	}

	for r := 0; r < 8; r++ {
		row := channelSwizzle[r]
		for c := 0; c < 8; c++ {
			v := d.Matrix[row][c] * gain
			mix.coeff[r*8+c] = C.OMX_U32(int32(v * 65536.0))
		}
	}

	mix.nPortIndex = 232
	Omxchk(C.omx_SetConfig_(d.Mixer.Handle,
		C.OMX_IndexConfigBrcmAudioDownmixCoefficients8x8, C.OMX_PTR(unsafe.Pointer(&mix))), "OMX_SetConfig")
}

// rpiAudioPortSettingsChanged — C: rpi_audio_port_settings_changed
// (rpi_audio.c:288-300)
func rpiAudioPortSettingsChanged(oc *OmxComponent) {
	d := oc.Opaque.(*rpiDecoder)
	ad := d.ad
	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)
	mp.Mutex.Lock()
	mb := mediacore.MediaBufAllocLocked(mp, 0)
	mb.DataType = int(mediacore.MBCtrlReconfigure)
	mb.Dtor = mediacore.MediaBufDtorFrameInfo
	mediacore.MbEnq(mp, mp.Audio, mb)
	mp.Mutex.Unlock()
}

// rpiAudioPortReconfigure — C: rpi_audio_port_reconfigure
// (rpi_audio.c:303-359)
func rpiAudioPortReconfigure(ad *audiocore.AudioDecoder) {
	d := getDecoder(ad)

	if d.RenderTun != nil {
		OmxTunnelDestroy(d.RenderTun)
		d.RenderTun = nil
	}
	if d.MixerTun != nil {
		OmxTunnelDestroy(d.MixerTun)
		d.MixerTun = nil
	}

	OmxSetState(d.Render, C.OMX_StateIdle)

	if d.Mixer != nil {
		OmxSetState(d.Mixer, C.OMX_StateIdle)

		// Copy PCM settings
		var pcm C.OMX_AUDIO_PARAM_PCMMODETYPE
		pcm.nSize = C.OMX_U32(unsafe.Sizeof(pcm))
		C.omx_init_version_(&pcm.nVersion)
		pcm.nPortIndex = 121

		Omxchk(C.omx_GetParameter_(d.Decoder.Handle,
			C.OMX_IndexParamAudioPcm, C.OMX_PTR(unsafe.Pointer(&pcm))), "OMX_GetParameter")

		pcm.nPortIndex = 231
		Omxchk(C.omx_SetParameter_(d.Mixer.Handle,
			C.OMX_IndexParamAudioPcm, C.OMX_PTR(unsafe.Pointer(&pcm))), "OMX_SetParameter")

		pcm.nPortIndex = 232
		Omxchk(C.omx_SetParameter_(d.Mixer.Handle,
			C.OMX_IndexParamAudioPcm, C.OMX_PTR(unsafe.Pointer(&pcm))), "OMX_SetParameter")

		d.Matrix = [8][8]float64{}
		for i := 0; i < 8; i++ {
			d.Matrix[i][i] = 1.0
		}

		setMixerMatrix(d)

		d.MixerTun = OmxTunnelCreate(d.Decoder, 121, d.Mixer, 232,
			"adecoder -> mixer")
		d.RenderTun = OmxTunnelCreate(d.Mixer, 231, d.Render, 100,
			"amixer -> render")
	} else {
		d.RenderTun = OmxTunnelCreate(d.Decoder, 121, d.Render, 100,
			"decoder -> render")
	}

	if d.Mixer != nil {
		OmxSetState(d.Mixer, C.OMX_StateExecuting)
	}
	OmxSetState(d.Render, C.OMX_StateExecuting)
}

// decoderSetup — C: decoder_init (rpi_audio.c:365-401)
func decoderSetup(d *rpiDecoder, withMixer bool) {
	ad := d.ad
	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)

	rpiAudioFini(ad)

	d.Render = OmxComponentCreate("OMX.broadcom.audio_render",
		&mp.Mutex, mp.Audio.Avail)

	if withMixer {
		d.Mixer = OmxComponentCreate("OMX.broadcom.audio_mixer",
			&mp.Mutex, mp.Audio.Avail)
	}

	d.LocalOutput = localOutput != 0

	device := "hdmi"
	if d.LocalOutput {
		device = "local"
	}

	rpiTS.Trace(trace.TRACE_DEBUG, "RPI", "Opening audio output %s", device)

	var audioDest C.OMX_CONFIG_BRCMAUDIODESTINATIONTYPE
	audioDest.nSize = C.OMX_U32(unsafe.Sizeof(audioDest))
	C.omx_init_version_(&audioDest.nVersion)
	cstr := C.CString(device)
	C.strncpy((*C.char)(unsafe.Pointer(&audioDest.sName[0])), cstr,
		C.strlen(cstr))
	C.free(unsafe.Pointer(cstr))
	Omxchk(C.omx_SetConfig_(d.Render.Handle,
		C.OMX_IndexConfigBrcmAudioDestination, C.OMX_PTR(unsafe.Pointer(&audioDest))), "OMX_SetConfig")

	if mp.Extra != nil {
		d.ClockTun = OmxTunnelCreate(OmxGetClock(mp), 80, d.Render, 101,
			"clock -> audio")
	}

	d.Decoder = OmxComponentCreate("OMX.broadcom.audio_decode",
		&mp.Mutex, mp.Audio.Avail)
}

// getOutSampleFormat — C: get_out_sample_format (rpi_audio.c:406-417)
func getOutSampleFormat(d *rpiDecoder) audiocore.SampleFormat {
	switch d.ad.InSampleFormat {
	case audiocore.SampleFormatFLTP, audiocore.SampleFormatFLT:
		return audiocore.SampleFormatFLTP
	default:
		return audiocore.SampleFormatS16
	}
}

// C: AV_CH_LAYOUT_MONO / STEREO / 7POINT1 values used by the Go port
// (audio core stores raw libav layout ints).
const (
	avChLayoutMono    = 0x4
	avChLayoutStereo  = 0x3
	avChLayout7Point1 = 0x3f
)

// getOutChannelLayout — C: get_out_channel_layout (rpi_audio.c:423-439)
func getOutChannelLayout(d *rpiDecoder) int64 {
	det := 0
	if d.Hdmi8chPcm && !d.LocalOutput {
		det = 1
	}
	switch d.ad.InChannelLayout {
	default:
		if checkMode(det, hdmi8chMode) != 0 {
			return avChLayout7Point1
		}
		fallthrough
	case avChLayoutMono, avChLayoutStereo:
		return avChLayoutStereo
	}
}

// rpiAudioConfigPcm — C: rpi_audio_config_pcm (rpi_audio.c:444-522)
func rpiAudioConfigPcm(d *rpiDecoder) int {
	ad := d.ad
	var wfe waveFormatExtensible
	var bps int

	ad.OutSampleFormat = getOutSampleFormat(d)

	switch ad.OutSampleFormat {
	case audiocore.SampleFormatFLTP:
		wfe.formatTag = 0x8000
		bps = 32
	case audiocore.SampleFormatS16:
		wfe.formatTag = 0x0001 // C: WAVE_FORMAT_PCM
		bps = 16
	default:
		panic("rpi_audio_config_pcm: bad format")
	}

	ad.OutChannelLayout = getOutChannelLayout(d)
	switch ad.OutChannelLayout {
	case avChLayoutStereo:
		d.Channels = 2
	case avChLayout7Point1:
		d.Channels = 8
	}

	// Configure input port
	ad.TileSize = 1024
	d.Bpf = d.Channels * (bps >> 3)

	var portParam C.OMX_PARAM_PORTDEFINITIONTYPE
	portParam.nSize = C.OMX_U32(unsafe.Sizeof(portParam))
	C.omx_init_version_(&portParam.nVersion)
	portParam.nPortIndex = 120

	Omxchk(C.omx_GetParameter_(d.Decoder.Handle, C.OMX_IndexParamPortDefinition,
		C.OMX_PTR(unsafe.Pointer(&portParam))), "OMX_GetParameter")

	C.pda(&portParam).eEncoding = C.OMX_AUDIO_CodingPCM
	portParam.nBufferSize = C.OMX_U32(d.Bpf * ad.TileSize)
	portParam.nBufferCountActual = 4

	Omxchk(C.omx_SetParameter_(d.Decoder.Handle,
		C.OMX_IndexParamPortDefinition, C.OMX_PTR(unsafe.Pointer(&portParam))), "OMX_SetParameter")

	// Configure input format
	var formatType C.OMX_AUDIO_PARAM_PORTFORMATTYPE
	formatType.nSize = C.OMX_U32(unsafe.Sizeof(formatType))
	C.omx_init_version_(&formatType.nVersion)
	formatType.nPortIndex = 120
	formatType.eEncoding = C.OMX_AUDIO_CodingPCM

	Omxchk(C.omx_SetParameter_(d.Decoder.Handle,
		C.OMX_IndexParamAudioPortFormat, C.OMX_PTR(unsafe.Pointer(&formatType))), "OMX_SetParameter")

	OmxSetState(d.Decoder, C.OMX_StateIdle)
	OmxAllocBuffers(d.Decoder, 120)
	OmxSetState(d.Decoder, C.OMX_StateExecuting)

	bytesPerSec := ad.InSampleRate * (bps >> 3) * d.Channels

	// C: wfe.Samples.wSamplesPerBlock = 0 (union member — shares
	// storage with wValidBitsPerSample which is overwritten below)
	wfe.channels = uint16(d.Channels)
	wfe.blockAlign = uint16(d.Channels * (bps >> 3))
	wfe.samplesPerSec = uint32(ad.InSampleRate)
	wfe.avgBytesPerSec = uint32(bytesPerSec)
	wfe.bitsPerSample = uint16(bps)
	wfe.validBitsPerSample = uint16(bps)
	wfe.cbSize = 0
	wfe.channelMask = uint32(ad.OutChannelLayout)
	wfe.subFormatTag = 0x0001 // C: KSDATAFORMAT_SUBTYPE_PCM.Data1
	wfe.subFormat = ksDataFormatSubTypePCM

	ad.OutSampleRate = ad.InSampleRate

	d.Decoder.PortSettingsChangedCb = rpiAudioPortSettingsChanged
	d.Decoder.Opaque = d

	buf := OmxGetBuffer(d.Decoder)
	buf.nOffset = 0
	wfeb := marshalWFE(&wfe)
	buf.nFilledLen = C.OMX_U32(len(wfeb))
	C.memcpy(unsafe.Pointer(buf.pBuffer), unsafe.Pointer(&wfeb[0]), C.size_t(buf.nFilledLen))
	buf.nFlags = C.OMX_BUFFERFLAG_CODECCONFIG | C.OMX_BUFFERFLAG_ENDOFFRAME
	Omxchk(C.omx_EmptyThisBuffer_(d.Decoder.Handle, buf), "OMX_EmptyThisBuffer")
	return 0
}

// rpiAudioReconfig — C: rpi_audio_reconfig (rpi_audio.c:528-545)
func rpiAudioReconfig(ad *audiocore.AudioDecoder) error {
	d := getDecoder(ad)

	if d.Decoder != nil {
		fmt_ := getOutSampleFormat(d)
		l := getOutChannelLayout(d)
		if fmt_ == ad.OutSampleFormat && l == ad.OutChannelLayout &&
			d.LocalOutput == (localOutput != 0) {
			return nil
		}
	}

	decoderSetup(d, true)
	rpiAudioConfigPcm(d)
	return nil
}

// rpiAudioDeliver — C: rpi_audio_deliver (rpi_audio.c:551-621)
func rpiAudioDeliver(ad *audiocore.AudioDecoder, samples int, pts int64,
	epoch int) int {
	d := getDecoder(ad)
	oc := d.Decoder

	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)

	if ad.Discontinuity && pts == mediacore.PTSUnset && mp.Extra != nil {
		ad.AVR.Read(nil, samples)
		return 0
	}

	buf := oc.Avail
	if buf == nil {
		return -1
	}
	oc.Avail = (*C.OMX_BUFFERHEADERTYPE)(buf.pAppPrivate)
	oc.InflightBuffers++
	oc.AvailBytes -= int(buf.nAllocLen)

	// C: assert(samples == ad->ad_tile_size)

	// C: avresample_read(ad->ad_avr, data, samples) — for FLTP the plane
	// pointers point at base + i*samples*4 (contiguous planes), so a
	// single contiguous read lands identically.
	out := unsafe.Slice((*byte)(buf.pBuffer), int(buf.nAllocLen))
	r := ad.AVR.Read(out[:samples*d.Bpf], samples)

	mp.Mutex.Unlock()

	if d.LastEpoch != epoch {
		if pts != mediacore.PTSUnset {
			d.LastEpoch = epoch
		}
		buf.nFlags |= C.OMX_BUFFERFLAG_DISCONTINUITY
	}

	if ad.Discontinuity {
		buf.nFlags |= C.OMX_BUFFERFLAG_STARTTIME | C.OMX_BUFFERFLAG_DISCONTINUITY
		ad.Discontinuity = false
	}

	buf.nOffset = 0
	buf.nFilledLen = C.OMX_U32(r * d.Bpf)

	if pts != mediacore.PTSUnset {
		buf.nTimeStamp = omxTicksFromS64(pts)
	} else {
		buf.nFlags |= C.OMX_BUFFERFLAG_TIME_UNKNOWN
	}

	if d.MasterVolume != masterVolume || d.MasterMute != float64(masterMute) {
		d.MasterVolume = masterVolume
		d.MasterMute = float64(masterMute)
		setMixerMatrix(d)
	}

	Omxchk(C.omx_EmptyThisBuffer_(oc.Handle, buf), "OMX_EmptyThisBuffer")

	if pts != mediacore.PTSUnset {
		updateAudioClock(ad, epoch)
	}

	mp.Mutex.Lock()

	if d.LocalOutput != (localOutput != 0) {
		ad.WantReconfig = true
	}
	return 0
}

// rpiAudioFlush — C: rpi_audio_flush (rpi_audio.c:626-641)
func rpiAudioFlush(ad *audiocore.AudioDecoder) {
	d := getDecoder(ad)
	if d.Decoder != nil {
		OmxFlushPort(d.Decoder, 121)
		OmxFlushPort(d.Decoder, 120)
	}
	if d.Mixer != nil {
		OmxFlushPort(d.Mixer, 231)
		OmxFlushPort(d.Mixer, 232)
	}
	if d.Render != nil {
		OmxFlushPort(d.Render, 100)
	}
}

// rpiAudioPause — C: rpi_audio_pause (rpi_audio.c:646-652)
func rpiAudioPause(ad *audiocore.AudioDecoder) {
	d := getDecoder(ad)
	if d.Render != nil {
		OmxSetState(d.Render, C.OMX_StatePause)
	}
}

// rpiAudioPlay — C: rpi_audio_play (rpi_audio.c:657-663)
func rpiAudioPlay(ad *audiocore.AudioDecoder) {
	d := getDecoder(ad)
	if d.Render != nil {
		OmxSetState(d.Render, C.OMX_StateExecuting)
	}
}

// rpiSetVolume — C: rpi_set_volume (rpi_audio.c:668-675)
func rpiSetVolume(ad *audiocore.AudioDecoder, scale float32) {
	d := getDecoder(ad)
	d.Gain = float64(scale)
	if d.Mixer != nil {
		setMixerMatrix(d)
	}
}

// rpiGetMode — C: rpi_get_mode (rpi_audio.c:680-820)
func rpiGetMode(ad *audiocore.AudioDecoder, codec int,
	extradata []byte) (audiocore.AudioMode, error) {
	d := getDecoder(ad)
	var encoding C.OMX_AUDIO_CODINGTYPE

	switch codec {
	case int(C.AV_CODEC_ID_FLAC):
		if !omxEnableFlac {
			return audiocore.ModePCM, nil
		}
		encoding = C.OMX_AUDIO_CodingFLAC

	case int(C.AV_CODEC_ID_VORBIS):
		if !omxEnableVorbis {
			return audiocore.ModePCM, nil
		}
		encoding = C.OMX_AUDIO_CodingVORBIS

	case int(C.AV_CODEC_ID_AC3):
		det := 0
		if d.HdmiAc3 && !d.LocalOutput {
			det = 1
		}
		if checkMode(det, hdmiAc3Mode) == 0 {
			return audiocore.ModePCM, nil
		}
		encoding = C.OMX_AUDIO_CodingDDP

	case int(C.AV_CODEC_ID_DTS):
		det := 0
		if d.HdmiDts && !d.LocalOutput {
			det = 1
		}
		if checkMode(det, hdmiDtsMode) == 0 {
			return audiocore.ModePCM, nil
		}
		encoding = C.OMX_AUDIO_CodingDTS

	default:
		return audiocore.ModePCM, nil
	}

	decoderSetup(d, false)

	var boolType C.OMX_CONFIG_BOOLEANTYPE
	boolType.nSize = C.OMX_U32(unsafe.Sizeof(boolType))
	C.omx_init_version_(&boolType.nVersion)
	boolType.bEnabled = C.OMX_TRUE
	Omxchk(C.omx_SetParameter_(d.Decoder.Handle,
		C.OMX_IndexParamBrcmDecoderPassThrough, C.OMX_PTR(unsafe.Pointer(&boolType))), "OMX_SetParameter")

	// Input port
	var portParam C.OMX_PARAM_PORTDEFINITIONTYPE
	portParam.nSize = C.OMX_U32(unsafe.Sizeof(portParam))
	C.omx_init_version_(&portParam.nVersion)
	portParam.nPortIndex = 120
	Omxchk(C.omx_GetParameter_(d.Decoder.Handle, C.OMX_IndexParamPortDefinition,
		C.OMX_PTR(unsafe.Pointer(&portParam))), "OMX_GetParameter")

	C.pda(&portParam).eEncoding = encoding
	portParam.nBufferSize = 49152
	portParam.nBufferCountActual = 4

	Omxchk(C.omx_SetParameter_(d.Decoder.Handle,
		C.OMX_IndexParamPortDefinition, C.OMX_PTR(unsafe.Pointer(&portParam))), "OMX_SetParameter")

	// Output port
	portParam.nPortIndex = 121
	Omxchk(C.omx_GetParameter_(d.Decoder.Handle, C.OMX_IndexParamPortDefinition,
		C.OMX_PTR(unsafe.Pointer(&portParam))), "OMX_GetParameter")
	Omxchk(C.omx_SetParameter_(d.Decoder.Handle,
		C.OMX_IndexParamPortDefinition, C.OMX_PTR(unsafe.Pointer(&portParam))), "OMX_SetParameter")

	var formatType C.OMX_AUDIO_PARAM_PORTFORMATTYPE
	formatType.nSize = C.OMX_U32(unsafe.Sizeof(formatType))
	C.omx_init_version_(&formatType.nVersion)
	formatType.nPortIndex = 120
	formatType.eEncoding = encoding

	Omxchk(C.omx_SetParameter_(d.Decoder.Handle,
		C.OMX_IndexParamAudioPortFormat, C.OMX_PTR(unsafe.Pointer(&formatType))), "OMX_SetParameter")

	OmxSetState(d.Decoder, C.OMX_StateIdle)
	OmxAllocBuffers(d.Decoder, 120)
	OmxSetState(d.Decoder, C.OMX_StateExecuting)

	if len(extradata) > 0 {
		buf := OmxGetBuffer(d.Decoder)
		buf.nOffset = 0
		buf.nFilledLen = C.OMX_U32(len(extradata))
		C.memset(unsafe.Pointer(buf.pBuffer), 0, C.size_t(buf.nAllocLen))
		C.memcpy(unsafe.Pointer(buf.pBuffer), unsafe.Pointer(&extradata[0]),
			C.size_t(buf.nFilledLen))
		buf.nFlags = C.OMX_BUFFERFLAG_CODECCONFIG | C.OMX_BUFFERFLAG_ENDOFFRAME
		Omxchk(C.omx_EmptyThisBuffer_(d.Decoder.Handle, buf),
			"OMX_EmptyThisBuffer")
	}

	d.Decoder.PortSettingsChangedCb = rpiAudioPortSettingsChanged
	d.Decoder.Opaque = d

	return audiocore.ModeCoded, nil
}

// rpiAudioDeliverCoded — C: rpi_audio_deliver_coded (rpi_audio.c:825-880)
func rpiAudioDeliverCoded(ad *audiocore.AudioDecoder, data []byte,
	pts int64, epoch int) int {
	d := getDecoder(ad)
	oc := d.Decoder

	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)

	if ad.Discontinuity && pts == mediacore.PTSUnset && mp.Extra != nil {
		return 0
	}

	buf := oc.Avail
	if buf == nil {
		return -1
	}
	oc.Avail = (*C.OMX_BUFFERHEADERTYPE)(buf.pAppPrivate)
	oc.InflightBuffers++
	oc.AvailBytes -= int(buf.nAllocLen)

	mp.Mutex.Unlock()

	C.memcpy(unsafe.Pointer(buf.pBuffer), unsafe.Pointer(&data[0]), C.size_t(len(data)))
	buf.nFilledLen = C.OMX_U32(len(data))
	buf.nOffset = 0

	if d.LastEpoch != epoch {
		if pts != mediacore.PTSUnset {
			d.LastEpoch = epoch
		}
		buf.nFlags |= C.OMX_BUFFERFLAG_DISCONTINUITY
	}

	if ad.Discontinuity {
		buf.nFlags |= C.OMX_BUFFERFLAG_STARTTIME | C.OMX_BUFFERFLAG_DISCONTINUITY
		ad.Discontinuity = false
	}

	if pts != mediacore.PTSUnset {
		buf.nTimeStamp = omxTicksFromS64(pts)
	} else {
		buf.nFlags |= C.OMX_BUFFERFLAG_TIME_UNKNOWN
	}
	buf.nFlags |= C.OMX_BUFFERFLAG_ENDOFFRAME

	Omxchk(C.omx_EmptyThisBuffer_(oc.Handle, buf), "OMX_EmptyThisBuffer")

	if pts != mediacore.PTSUnset {
		updateAudioClock(ad, epoch)
	}

	mp.Mutex.Lock()
	return 0
}

// setMastervol — C: set_mastervol (rpi_audio.c:902-906)
func setMastervol(opaque any, value float64) {
	masterVolume = math.Pow(10, value/20)
}

// setMastermute — C: set_mastermute (rpi_audio.c:911-915)
func setMastermute(opaque any, value int) {
	masterMute = value
}

// rpiAudioDriverStart — C: audio_driver_init (rpi_audio.c:920-973)
func rpiAudioDriverStart(asettings *propcore.Prop,
	sm *settingscore.SettingsManager) *audiocore.AudioClass {
	if sm != nil && asettings != nil {
		sm.SettingCreate(settingscore.SettingMultiOpt, asettings,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, sm.P("Audio output port"),
			settingscore.SettingTagStore, "audio2", "outputport",
			settingscore.SettingTagWriteInt, &localOutput,
			settingscore.SettingTagOption, "0", sm.P("HDMI"),
			settingscore.SettingTagOption, "1", sm.P("Analog"),
		)

		sm.SettingCreate(settingscore.SettingMultiOpt, asettings,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, sm.P("8 Channel PCM"),
			settingscore.SettingTagStore, "audio2", "8chmode",
			settingscore.SettingTagWriteInt, &hdmi8chMode,
			settingscore.SettingTagOption, "0", sm.P("Autodetect"),
			settingscore.SettingTagOption, "1", sm.P("Off"),
			settingscore.SettingTagOption, "2", sm.P("On"),
		)

		sm.SettingCreate(settingscore.SettingMultiOpt, asettings,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, sm.P("AC3 Pass-Through"),
			settingscore.SettingTagStore, "audio2", "ac3mode",
			settingscore.SettingTagWriteInt, &hdmiAc3Mode,
			settingscore.SettingTagOption, "0", sm.P("Autodetect"),
			settingscore.SettingTagOption, "1", sm.P("Off"),
			settingscore.SettingTagOption, "2", sm.P("On"),
		)

		sm.SettingCreate(settingscore.SettingMultiOpt, asettings,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, sm.P("DTS Pass-Through"),
			settingscore.SettingTagStore, "audio2", "dtsmode",
			settingscore.SettingTagWriteInt, &hdmiDtsMode,
			settingscore.SettingTagOption, "0", sm.P("Autodetect"),
			settingscore.SettingTagOption, "1", sm.P("Off"),
			settingscore.SettingTagOption, "2", sm.P("On"),
		)
	}

	// C: prop_subscribe(0, PROP_TAG_CALLBACK_FLOAT, set_mastervol, NULL,
	//   PROP_TAG_NAME("global", "audio", "mastervolume"), NULL)
	var pm *propcore.PropManager
	if asettings != nil {
		pm = asettings.Manager()
	}
	if pm != nil {
		global := pm.GetGlobal()
		propcore.SubscribePath(global, []string{"audio", "mastervolume"},
			false, func(ev propcore.EventType, args ...any) {
				v, deliver := trampolineFloat(ev, args)
				if deliver {
					setMastervol(nil, v)
				}
			})

		// C: prop_subscribe(0, PROP_TAG_CALLBACK_INT, set_mastermute, NULL,
		//   PROP_TAG_NAME("global", "audio", "mastermute"), NULL)
		propcore.SubscribePath(global, []string{"audio", "mastermute"},
			false, func(ev propcore.EventType, args ...any) {
				if v, deliver := propcore.TrampolineInt(ev, args, false); deliver {
					setMastermute(nil, v)
				}
			})
	}

	// C: return &rpi_audio_class (rpi_audio.c:885-900)
	return &audiocore.AudioClass{
		Start:              rpiAudioStart,
		Fini:               rpiAudioFini,
		Reconfig:           rpiAudioReconfig,
		DeliverLocked:      rpiAudioDeliver,
		Flush:              rpiAudioFlush,
		Pause:              rpiAudioPause,
		Play:               rpiAudioPlay,
		SetVolume:          rpiSetVolume,
		GetMode:            rpiGetMode,
		DeliverCodedLocked: rpiAudioDeliverCoded,
		Reconfigure:        rpiAudioPortReconfigure,
	}
}

// trampolineFloat — C: trampoline_float (prop_core.c:594-611): int→v,
// float→v, everything else→0.
func trampolineFloat(ev propcore.EventType, args []any) (float64, bool) {
	if len(args) == 0 {
		return 0, true
	}
	switch ev {
	case propcore.EventSetFloat:
		if f, ok := args[0].(float64); ok {
			return f, true
		}
		if f, ok := args[0].(float32); ok {
			return float64(f), true
		}
	case propcore.EventSetInt:
		if i, ok := args[0].(int); ok {
			return float64(i), true
		}
	}
	return 0, true
}

func init() {
	audiocore.SetPlatformDriverStart(func(settings any) (*audiocore.AudioClass, error) {
		as, _ := settings.(*propcore.Prop)
		return rpiAudioDriverStart(as, audiocore.PlatformSettingsMgr()), nil
	})
}
