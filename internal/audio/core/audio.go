package core

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// AudioManager manages audio system state
type AudioManager struct {
	masterVolume float32
	masterMute   int32
	audioIDTally int32
	audioClass   *AudioClass
	mutex        sync.RWMutex
	pm           *propcore.PropManager

	// settingsMgr creates audio settings (C: settings_add_dir, setting_create)
	settingsMgr *settingscore.SettingsManager

	// AV volume/sync settings (C: gconf.setting_av_volume, gconf.setting_av_sync)
	settingAVVolume *settingscore.Setting
	settingAVSync   *settingscore.Setting

	// C: prop nodes created by audio_mastervol_init (mv/mm)
	mastervolumeProp *propcore.Prop
	mastermuteProp   *propcore.Prop
	store            *htsmsg.Store                     // C: global htsmsg_store — injected
	fam              *fileaccesscore.FileAccessManager // C: implicit global fa context — injected
	ts               *trace.TraceSystem                // C: trace() global — injected
	gc               *gconf.T                          // C: gconf_t — injected
}

// PlatformDriverInit — Go seam for C's audio_driver_init being a
// link-time symbol provided by the platform TU (e.g. rpi_audio.c on
// RPi). arch/rpi registers it via init() under the rpi build tag.
func SetPlatformDriverStart(fn func(settings any) (*AudioClass, error)) {
	audioDeps.platformDriverStart = fn
}

// audioDeps — link-time seams resolved at build-tag selection in C
// (platform audio_driver_init TU, CONFIG_GLW_REC sender) + the settings
// manager reachable by platform drivers that can't see AudioManager.
var audioDeps struct {
	pulseUserError      func(msg string) // C: pulse error popup — linux tag
	platformDriverStart func(settings any) (*AudioClass, error)
	glwRecSend          func(ad *AudioDecoder, framePCM []byte, frameNb int, pts int64)
	platformSettingsMgr *settingscore.SettingsManager
	ts                  *trace.TraceSystem // C: trace() global — injected

	// paState — C: pulseaudio.c file-statics (user_errmsg flag +
	// stream-callback handle table). Lives on the dep seam because
	// the //export cgo callbacks are free functions without userdata.
	paErrSent  bool
	paHandlesM sync.Mutex
	// paHandles — *pulseDecoder under the `pulse` tag; `any` keeps
	// audio.go build-tag-free.
	paHandles map[uintptr]any
	paNextH   uintptr
}

// PlatformSettingsMgr — the settings manager active during
// audioDriverStartPlatform. In C, setting_create resolves the global
// settings module directly; in Go platform drivers need the manager to
// build SETTING_MULTIOPT entries (rpi_audio.c uses it for the
// output-port/passthrough settings).
func PlatformSettingsMgr() *settingscore.SettingsManager {
	return audioDeps.platformSettingsMgr
}

// NewAudioManager creates a new audio manager
// SetTraceSystem injects the trace system (C: trace() global).
func (am *AudioManager) SetTraceSystem(ts *trace.TraceSystem) {
	am.ts = ts
	audioDeps.ts = ts // C: file-static trace reachable by //export callbacks
}

// SetFAM injects the file access manager (C: implicit global fa context).
func (am *AudioManager) SetFAM(fam *fileaccesscore.FileAccessManager) { am.fam = fam }

// SetGconf — C: implicit gconf_t access.
func (am *AudioManager) SetGconf(gc *gconf.T) { am.gc = gc }

func NewAudioManager(pm *propcore.PropManager,
	store *htsmsg.Store) *AudioManager {
	return &AudioManager{
		store:        store,
		masterVolume: 1.0,
		masterMute:   0,
		audioIDTally: 0,
		audioClass:   nil,
		pm:           pm,
	}
}

// AudioMode represents the audio output mode
type AudioMode int

const (
	ModePCM AudioMode = iota
	ModeSPDIF
	ModeCoded
)

// SampleFormat represents audio sample format
type SampleFormat int

const (
	SampleFormatU8 SampleFormat = iota
	SampleFormatS16
	SampleFormatS32
	SampleFormatFLT
	SampleFormatDBL
	SampleFormatU8P
	SampleFormatS16P
	SampleFormatS32P
	SampleFormatFLTP
	SampleFormatDBLP
)

// Channel layout constants
const (
	ChannelLayoutMono        = 0x00000004
	ChannelLayoutStereo      = 0x00000003
	ChannelLayout2Point1     = 0x00000013
	ChannelLayoutSurround    = 0x00000033
	ChannelLayout4Point0     = 0x00000034
	ChannelLayoutQuad        = 0x00000035
	ChannelLayout5Point0     = 0x00000037
	ChannelLayout5Point1     = 0x0000003f
	ChannelLayout5Point0Back = 0x00000038
	ChannelLayout5Point1Back = 0x00000040
	ChannelLayout7Point0     = 0x00000063
	ChannelLayout7Point1     = 0x0000006f
	ChannelLayout7Point1Wide = 0x00000070
)

// AudioClass represents an audio driver class
type AudioClass struct {
	AllocSize int

	Start       func(ad *AudioDecoder) error
	Fini        func(ad *AudioDecoder)
	Reconfig    func(ad *AudioDecoder) error
	Reconfigure func(ad *AudioDecoder)
	// C: ac_deliver_unlocked / ac_deliver_locked return int —
	// 0 = delivered, -1 = wait indefinitely on mq_avail,
	// N > 0 = wait N ms on mq_avail (audio.c:653-665).
	DeliverUnlocked func(ad *AudioDecoder, samples int, pts int64, epoch int) int
	DeliverLocked   func(ad *AudioDecoder, samples int, pts int64, epoch int) int

	Pause   func(ad *AudioDecoder)
	Play    func(ad *AudioDecoder)
	Flush   func(ad *AudioDecoder)
	GetMode func(ad *AudioDecoder, codec int, extradata []byte) (AudioMode, error)

	SetVolume func(ad *AudioDecoder, scale float32)

	// C: ac_deliver_coded_locked returns int — non-zero means the data was
	// not accepted and the buffer is re-queued (audio.c:693-707).
	DeliverCodedLocked func(ad *AudioDecoder, data []byte, pts int64, epoch int) int
}

// SetGlwRecAudioSend wires glw_rec_audio_send (C: glw_rec.c:429 —
// invoked from audio.c only under CONFIG_GLW_REC). The glw package
// registers it via init() under the "glwrec" build tag; nil means the
// feature is compiled out, matching the C #if gating.
func SetGlwRecAudioSend(fn func(ad *AudioDecoder, framePCM []byte, frameNb int, pts int64)) {
	audioDeps.glwRecSend = fn
}

// AudioDecoder represents an audio decoder
type AudioDecoder struct {
	Class *AudioClass
	ID    int

	ts *trace.TraceSystem // C: trace() global — from owning manager

	Mode AudioMode

	Frame []byte
	PTS   int64
	Epoch int

	Discontinuity bool

	TileSize int // Number of samples to be delivered per round
	Delay    int // Audio output delay in us

	Bitrate int // Estimated audio bitrate in bps

	SampleRateFail    bool
	ChannelLayoutFail bool

	Paused bool

	InCodecID       int
	InSampleRate    int
	InSampleFormat  SampleFormat
	InChannelLayout int64

	OutSampleRate    int
	OutSampleFormat  SampleFormat
	OutChannelLayout int64

	StereoDownmix bool // We can only output stereo so ask for downmix

	AVR *AudioResampler

	MuxBuffer []byte

	SPDIFMuxer *SPDIFMuxer

	SPDIFFrame      []byte
	SPDIFFrameSize  int
	SPDIFFrameAlloc int

	VolScale     float32
	WantReconfig bool

	// Bitrate computation
	FrameSize         [16]int
	FrameSizePtr      int
	EstimatedDuration int

	LastPTS  int64
	SavedPTS int64

	// Threading
	mutex      sync.Mutex
	cond       *sync.Cond
	ctx        context.Context
	cancel     context.CancelFunc
	isRunning  bool
	threadDone chan struct{} // closed when audioDecodeThread returns

	// Media pipe integration
	MediaPipe any // Will be connected to media pipe

	// C: the decoder's owning manager (for PropManager access)
	manager *AudioManager

	// Audio instance for platform-specific audio (e.g., ALSA)
	AudioInstance any // Platform-specific audio instance

	// decScratch — reused PCM output buffer for the codec decode
	// (one alloc instead of a fresh 8MB per packet).
	decScratch []byte
}

// getBytesPerSample calculates bytes per sample based on format and channels
func getBytesPerSample(format SampleFormat, channels int) int {
	bytesPerChannel := 0
	switch format {
	case SampleFormatU8, SampleFormatU8P:
		bytesPerChannel = 1
	case SampleFormatS16, SampleFormatS16P:
		bytesPerChannel = 2
	case SampleFormatS32, SampleFormatS32P, SampleFormatFLT, SampleFormatFLTP:
		bytesPerChannel = 4
	case SampleFormatDBL, SampleFormatDBLP:
		bytesPerChannel = 8
	default:
		bytesPerChannel = 2 // Default to 16-bit
	}
	return bytesPerChannel * channels
}

// AudioResampler handles audio resampling and format conversion using FFmpeg swresample
type AudioResampler struct {
	inSampleRate    int
	outSampleRate   int
	inSampleFormat  SampleFormat
	outSampleFormat SampleFormat
	inChannels      int
	outChannels     int
	swrContext      *libav.SwrContext
	buffer          []byte
	bufferSize      int
	bytesPerSample  int    // Calculated from format and channels
	outBuf          []byte // C: avresample internal output FIFO
}

// SPDIFMuxer handles SPDIF pass-through muxing using FFmpeg
type SPDIFMuxer struct {
	codecID    int
	sampleRate int
	muxer      *libav.SPDIFMuxerContext
	buffer     []byte
	bufferSize int
}

// NewAudioResampler creates a new audio resampler with FFmpeg swresample
func NewAudioResampler(inRate, outRate int, inFmt, outFmt SampleFormat, inCh, outCh int) *AudioResampler {
	ar := &AudioResampler{
		inSampleRate:    inRate,
		outSampleRate:   outRate,
		inSampleFormat:  inFmt,
		outSampleFormat: outFmt,
		inChannels:      inCh,
		outChannels:     outCh,
		bufferSize:      4096,
		bytesPerSample:  getBytesPerSample(outFmt, outCh),
	}

	// Initialize swresample context if conversion is needed
	if inRate != outRate || inFmt != outFmt || inCh != outCh {
		// Convert sample formats to FFmpeg AVSampleFormat
		inFmtFF := SampleFormatToAV(inFmt)
		outFmtFF := SampleFormatToAV(outFmt)

		// Convert channel counts to channel layouts
		inLayout := channelsToLayout(inCh)
		outLayout := channelsToLayout(outCh)

		// Allocate and configure swresample context
		ar.swrContext = libav.SwrAllocSetOpts(
			int64(outLayout),
			int(outFmtFF),
			outRate,
			int64(inLayout),
			int(inFmtFF),
			inRate,
		)

		if ar.swrContext != nil {
			if err := ar.swrContext.Start(); err != nil {
				// If initialization fails, free the context
				ar.swrContext.Free()
				ar.swrContext = nil
			}
		}
	}

	return ar
}

// isPlanarSampleFormat — C: av_sample_fmt_is_planar.
func isPlanarSampleFormat(f SampleFormat) bool {
	switch f {
	case SampleFormatU8P, SampleFormatS16P, SampleFormatS32P,
		SampleFormatFLTP, SampleFormatDBLP:
		return true
	}
	return false
}

// SampleFormatToAV converts SampleFormat to FFmpeg AVSampleFormat
func SampleFormatToAV(fmt SampleFormat) int {
	switch fmt {
	case SampleFormatU8:
		return 0 // AV_SAMPLE_FMT_U8
	case SampleFormatS16:
		return 1 // AV_SAMPLE_FMT_S16
	case SampleFormatS32:
		return 2 // AV_SAMPLE_FMT_S32
	case SampleFormatFLT:
		return 3 // AV_SAMPLE_FMT_FLT
	case SampleFormatDBL:
		return 4 // AV_SAMPLE_FMT_DBL
	case SampleFormatU8P:
		return 5 // AV_SAMPLE_FMT_U8P
	case SampleFormatS16P:
		return 6 // AV_SAMPLE_FMT_S16P
	case SampleFormatS32P:
		return 7 // AV_SAMPLE_FMT_S32P
	case SampleFormatFLTP:
		return 8 // AV_SAMPLE_FMT_FLTP
	case SampleFormatDBLP:
		return 9 // AV_SAMPLE_FMT_DBLP
	default:
		return 1 // Default to S16
	}
}

// channelsToLayout converts channel count to FFmpeg channel layout
func channelsToLayout(channels int) int64 {
	switch channels {
	case 1:
		return 4 // AV_CH_LAYOUT_MONO
	case 2:
		return 3 // AV_CH_LAYOUT_STEREO
	case 3:
		return 0x00000007 // AV_CH_LAYOUT_2POINT1
	case 4:
		return 0x00000003 | 0x00000010 // AV_CH_LAYOUT_2.1 (or 4.0)
	case 5:
		return 0x00000037 // AV_CH_LAYOUT_4POINT1
	case 6:
		return 0x0000003F // AV_CH_LAYOUT_5POINT1
	case 7:
		return 0x00000060 | 0x0000003F // AV_CH_LAYOUT_6POINT1
	case 8:
		return 0x00000063 | 0x0000003F // AV_CH_LAYOUT_7POINT1
	default:
		return 3 // Default to stereo
	}
}

// convert performs swresample conversion of `input` (`samples` input
// sample-frames) and returns the produced output bytes.
// C: swr_convert (the inner half of avresample_convert).
func (ar *AudioResampler) convert(input []byte, samples int) []byte {
	// If no conversion needed, return as-is
	if ar.inSampleRate == ar.outSampleRate && ar.inSampleFormat == ar.outSampleFormat && ar.inChannels == ar.outChannels {
		return input
	}

	// If swresample context is not available, fall back to simple pass-through
	if ar.swrContext == nil {
		return input
	}

	// Calculate output buffer size based on rate conversion
	ratio := float64(ar.outSampleRate) / float64(ar.inSampleRate)
	outSamples := int(float64(samples)*ratio) + 64 // + output delay slack
	bytesPerSample := ar.bytesPerSample

	outputSize := outSamples * bytesPerSample
	if ar.buffer == nil || len(ar.buffer) < outputSize {
		ar.buffer = make([]byte, outputSize)
	}

	// Prepare input and output buffers for swresample.
	// C: avresample_convert(avr, NULL, 0, 0, frame->data, frame->linesize[0],
	//   nb) — passes the AVFrame plane array, so planar formats get one
	//   pointer per channel. decode_audio_frame flattened the planes
	//   contiguously into `input`; split it back into per-plane slices or
	//   swr_convert reads past the array for in_planes > 1.
	inData := [][]byte{input}
	if isPlanarSampleFormat(ar.inSampleFormat) && ar.inChannels > 1 {
		planeSize := len(input) / ar.inChannels
		inData = make([][]byte, ar.inChannels)
		for i := range inData {
			inData[i] = input[i*planeSize:]
		}
	}
	outData := [][]byte{ar.buffer[:outputSize]}

	// Perform resampling
	convertedSamples, err := ar.swrContext.Convert(outData, outSamples, inData, samples)
	if err != nil {
		return nil
	}

	return ar.buffer[:convertedSamples*bytesPerSample]
}

// Convert converts input samples and appends the output to the resampler's
// internal output FIFO.
// C: avresample_convert(ad->ad_avr, NULL, 0, 0, data, linesize, nb)
// (audio.c:602-604).
func (ar *AudioResampler) Convert(input []byte, samples int) {
	if out := ar.convert(input, samples); len(out) > 0 {
		ar.outBuf = append(ar.outBuf, out...)
	}
}

// Resample converts input and returns the produced output directly
// (Go-compat non-buffered variant used by ProcessAudioFrame/tests).
func (ar *AudioResampler) Resample(input []byte, samples int) ([]byte, int) {
	out := ar.convert(input, samples)
	if ar.bytesPerSample == 0 {
		return out, samples
	}
	return out, len(out) / ar.bytesPerSample
}

// Available returns the number of output sample-frames buffered in the
// resampler. C: avresample_available(ad->ad_avr).
func (ar *AudioResampler) Available() int {
	if ar.bytesPerSample == 0 {
		return 0
	}
	return len(ar.outBuf) / ar.bytesPerSample
}

// Read reads `samples` output sample-frames from the resampler FIFO into
// output (nil output discards).
// C: avresample_read(ad->ad_avr, buf, samples) (audio.c:743-745).
func (ar *AudioResampler) Read(output []byte, samples int) int {
	if ar.bytesPerSample == 0 {
		return 0
	}
	bytesToRead := min(samples*ar.bytesPerSample, len(ar.outBuf))
	if output != nil {
		copy(output, ar.outBuf[:bytesToRead])
	}
	ar.outBuf = ar.outBuf[bytesToRead:]
	return bytesToRead / ar.bytesPerSample
}

// GetDelay returns the resampler's input-side delay in input samples.
// C: avresample_get_delay(ad->ad_avr) (audio.c:517).
func (ar *AudioResampler) GetDelay() int64 {
	if ar.swrContext == nil {
		return 0
	}
	d, err := ar.swrContext.GetDelay(int64(ar.inSampleRate))
	if err != nil {
		return 0
	}
	return d
}

// Flush flushes the resampler output FIFO.
// C: avresample_read drain / resampler recreation.
func (ar *AudioResampler) Flush() {
	ar.outBuf = nil
}

// Close releases the resampler context — C: avresample_close +
// avresample_free(&ad->ad_avr) (audio.c:217-220).
func (ar *AudioResampler) Close() {
	if ar.swrContext != nil {
		ar.swrContext.Free()
		ar.swrContext = nil
	}
	ar.outBuf = nil
}

// NewSPDIFMuxer creates a new SPDIF muxer
func NewSPDIFMuxer(codecID, sampleRate int) *SPDIFMuxer {
	muxer := libav.SPDIFMuxerCreate(codecID, sampleRate)
	return &SPDIFMuxer{
		codecID:    codecID,
		sampleRate: sampleRate,
		muxer:      muxer,
		bufferSize: 16384,
	}
}

// Mux muxes audio data for SPDIF output using FFmpeg
func (sm *SPDIFMuxer) Mux(data []byte) []byte {
	if sm.muxer == nil {
		return data
	}

	if len(data) == 0 {
		return nil
	}

	// Reset previous data
	sm.muxer.ResetData()

	// Mux the packet
	if err := sm.muxer.MuxPacket(data); err != nil {
		// On error, return original data
		return data
	}

	// Get muxed data
	return sm.muxer.GetData()
}

// Close closes the SPDIF muxer
func (sm *SPDIFMuxer) Close() {
	if sm.muxer != nil {
		sm.muxer.Destroy()
		sm.muxer = nil
	}
}

// GetMasterVolume — C: audio_master_volume (audio.c:47).
func (am *AudioManager) GetMasterVolume() float32 {
	am.mutex.RLock()
	defer am.mutex.RUnlock()
	return am.masterVolume
}

// GetMasterMute — C: audio_master_mute (audio.c:48).
func (am *AudioManager) GetMasterMute() int32 {
	am.mutex.RLock()
	defer am.mutex.RUnlock()
	return am.masterMute
}

// saveMatervol — C: save_matervol (audio.c:56-71). dB → linear scale,
// htsmsg_store_save(map{master-volume: value*1000}, "audiomixer").
func (am *AudioManager) saveMatervol(value float32) {
	am.mutex.Lock()
	am.masterVolume = float32(math.Pow(10, float64(value)/20))
	am.mutex.Unlock()

	if am.store == nil {
		return
	}
	m := htsmsg.NewMap()
	m.AddS32("master-volume", int32(value*1000))
	am.store.Save(m, "audiomixer")
	m.Release()
}

// audioMastervolSetup — C: audio_mastervol_init (audio.c:74-108).
// Creates global.audio.{mastervolume,mastermute} props, loads the
// persisted dB value from htsmsg_store("audiomixer"), and subscribes
// the float callback (save_matervol) and SET_INT → audio_master_mute.
func (am *AudioManager) audioMastervolSetup() {
	// C: htsmsg_t *m = htsmsg_store_load("audiomixer")
	var m *htsmsg.HTSMsg
	if am.store != nil {
		m, _ = am.store.Load("audiomixer")
	}

	// C: pa = prop_create(prop_get_global(), "audio");
	//    mv = prop_create(pa, "mastervolume");
	//    mm = prop_create(pa, "mastermute")
	pa := am.pm.CreateEx(am.pm.GetGlobal(), "audio", nil, false, false)
	mv := am.pm.CreateEx(pa, "mastervolume", nil, false, false)
	mm := am.pm.CreateEx(pa, "mastermute", nil, false, false)
	am.mastervolumeProp = mv
	am.mastermuteProp = mm

	// C: prop_set_float_clipping_range(mv, -75, 12)
	mv.SetClippedFloat(-75, 12)

	// C: if(m != NULL && !htsmsg_get_s32(m, "master-volume", &i32))
	if m != nil {
		if i32, err := m.GetS32("master-volume"); err == nil {
			f := float32(i32) / 1000
			mv.SetFloat(f)
			// am.mutex already held by AudioStart (sole caller)
			am.masterVolume = float32(math.Pow(10, float64(f)/20))
		}
		m.Release()
	}

	// C: prop_set_int(mm, 0)
	mm.SetInt(0)

	// C: prop_subscribe(PROP_SUB_NO_INITIAL_UPDATE,
	//     PROP_TAG_CALLBACK_FLOAT, save_matervol, NULL,
	//     PROP_TAG_ROOT, mv, NULL)
	mv.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
		if event != propcore.EventSetFloat || len(args) == 0 {
			return
		}
		switch v := args[0].(type) {
		case float32:
			am.saveMatervol(v)
		case float64:
			am.saveMatervol(float32(v))
		}
	}, nil, propcore.SubNoInitialUpdate)

	// C: prop_subscribe(PROP_SUB_NO_INITIAL_UPDATE,
	//     PROP_TAG_SET_INT, &audio_master_mute,
	//     PROP_TAG_ROOT, mm, NULL)
	// C delivers through trampoline_int_set: SET_FLOAT/SET_STRING
	// convert to int, any other event stores 0.
	mm.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
		if v, deliver := propcore.TrampolineInt(event, args, false); deliver {
			am.mutex.Lock()
			am.masterMute = int32(v)
			am.mutex.Unlock()
		}
	}, nil, propcore.SubNoInitialUpdate)
}

// SetMasterVolume writes the mastervolume prop (dB derived from the
// linear scale) and updates audio_master_volume — the Go-compat
// equivalent of the UI driving the mv prop.
func (am *AudioManager) SetMasterVolume(vol float32) {
	am.mutex.Lock()
	am.masterVolume = vol
	mv := am.mastervolumeProp
	am.mutex.Unlock()
	if mv != nil {
		dB := float32(20 * math.Log10(float64(vol)))
		mv.SetFloat(dB)
	}
}

// SetMasterMute writes the mastermute prop — the Go-compat equivalent
// of the UI driving the mm prop.
func (am *AudioManager) SetMasterMute(mute int32) {
	am.mutex.Lock()
	am.masterMute = mute
	mm := am.mastermuteProp
	am.mutex.Unlock()
	if mm != nil {
		mm.SetInt(int(mute))
	}
}

// SetSettingsManager sets the settings manager for creating audio settings.
func (am *AudioManager) SetSettingsManager(sm *settingscore.SettingsManager) {
	am.mutex.Lock()
	defer am.mutex.Unlock()
	am.settingsMgr = sm
}

// AudioStart initializes the audio subsystem.
// C: audio_init (audio.c:112-150) — creates "Audio settings" directory,
// calls audio_mastervol_init, audio_driver_init, creates separator
// "Video playback", and AV volume/sync settings.
func (am *AudioManager) AudioStart() error {
	am.mutex.Lock()
	defer am.mutex.Unlock()

	// C: prop_t *asettings = settings_add_dir(NULL, "Audio settings", "sound",
	//   NULL, "Setup audio output", "settings:audio")
	var asettings *propcore.Prop
	if am.settingsMgr != nil {
		asettings = am.settingsMgr.AddDir(nil, am.settingsMgr.P("Audio settings"), "sound",
			"", am.settingsMgr.P("Setup audio output"), "settings:audio")
	}

	// C: audio_mastervol_init()
	am.audioMastervolSetup()

	// C: gconf.setting_av_volume / gconf.setting_av_sync are assigned
	// in audio_init and consumed by media_settings.c prop bindings.
	if am.gc != nil {
		am.gc.SettingAVVolume = nil
		am.gc.SettingAVSync = nil
	}

	// C: audio_class = audio_driver_init(asettings)
	audioDeps.platformSettingsMgr = am.settingsMgr
	var err error
	am.audioClass, err = audioDriverStartPlatform(asettings)
	if err != nil {
		return fmt.Errorf("failed to initialize audio driver: %w", err)
	}

	// C: settings_create_separator(asettings, "Video playback")
	if am.settingsMgr != nil && asettings != nil {
		am.settingsMgr.CreateSeparatorProp(asettings,
			am.pm.CreateMulti(asettings, "title"))

		// C: gconf.setting_av_volume = setting_create(SETTING_INT, asettings,
		//   SETTINGS_INITIAL_UPDATE, SETTING_TITLE(_p("Audio gain")),
		//   SETTING_RANGE(-12, 12), SETTING_UNIT_CSTR("dB"),
		//   SETTING_STORE("audio2", "videovolume"),
		//   SETTING_VALUE_ORIGIN("global"), NULL)
		am.settingAVVolume = am.settingsMgr.SettingCreate(
			settingscore.SettingInt, asettings,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, am.settingsMgr.P("Audio gain"),
			settingscore.SettingTagRange, -12, 12,
			settingscore.SettingTagUnitCStr, "dB",
			settingscore.SettingTagStore, "audio2", "videovolume",
			settingscore.SettingTagValueOrigin, "global",
			nil)
		if am.gc != nil {
			am.gc.SettingAVVolume = am.settingAVVolume
		}

		// C: gconf.setting_av_sync = setting_create(SETTING_INT, asettings,
		//   SETTINGS_INITIAL_UPDATE, SETTING_TITLE(_p("Audio delay")),
		//   SETTING_RANGE(-5000, 5000), SETTING_STEP(50),
		//   SETTING_UNIT_CSTR("ms"), SETTING_STORE("audio2", "avdelta"),
		//   SETTING_VALUE_ORIGIN("global"), NULL)
		am.settingAVSync = am.settingsMgr.SettingCreate(
			settingscore.SettingInt, asettings,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, am.settingsMgr.P("Audio delay"),
			settingscore.SettingTagRange, -5000, 5000,
			settingscore.SettingTagStep, 50,
			settingscore.SettingTagUnitCStr, "ms",
			settingscore.SettingTagStore, "audio2", "avdelta",
			settingscore.SettingTagValueOrigin, "global",
			nil)

		// C: #if CONFIG_AUDIOTEST audio_test_init(asettings) (audio.c:148)
		audiotestSetup(am, asettings)
	}

	return nil
}

// audioDriverStartPlatform selects the appropriate audio driver based on platform.
// On Linux, returns an ALSA-backed AudioClass that mirrors C's alsa_audio_class.
// The AudioClass is used by the core audio system for volume, pause, flush, etc.
// The PlaybackPipeline also creates its own ALSAAudio instance directly for
// sample delivery, but the core AudioClass provides the architectural interface.
// audioDriverStartPlatform — C: audio_driver_init (audio.h:128). On
// Linux this is provided per-build-tag by alsa_linux.go (canonical
// default: CONFIG_LIBASOUND wins over libpulse), pulse_linux.go
// (`pulse` tag = CONFIG_LIBPULSE without libasound), or
// dummy_audio.go (`dummyaudio` tag / non-linux fallback).

// newAudioDecoder allocates an audio_decoder_t — C: the calloc'd
// portion of audio_decoder_create (audio.c:165-196).
func (am *AudioManager) newAudioDecoder(class *AudioClass) *AudioDecoder {
	id := int(atomic.AddInt32(&am.audioIDTally, 1))
	ad := &AudioDecoder{
		Class:            class,
		ID:               id,
		ts:               am.ts,
		Mode:             ModePCM,
		TileSize:         1024,
		Delay:            0,
		VolScale:         1.0,
		WantReconfig:     false,
		PTS:              mediacore.PTSUnset,
		Epoch:            0,
		OutSampleRate:    48000,
		OutSampleFormat:  SampleFormatS16,
		OutChannelLayout: ChannelLayoutStereo,
	}
	ad.cond = sync.NewCond(&ad.mutex)
	ad.ctx, ad.cancel = context.WithCancel(context.Background())
	ad.threadDone = make(chan struct{})
	ad.manager = am
	return ad
}

// AudioDecoderCreate creates and starts an audio decoder
func (am *AudioManager) AudioDecoderCreate(mediaPipe any) (*AudioDecoder, error) {
	am.mutex.RLock()
	class := am.audioClass
	am.mutex.RUnlock()

	if class == nil {
		return nil, fmt.Errorf("audio class not initialized")
	}

	ad := am.newAudioDecoder(class)
	ad.MediaPipe = mediaPipe

	// C: audio_decoder_create only allocates + spawns the thread;
	// ac->ac_init runs inside audio_decode_thread (audio.c:630).
	ad.mutex.Lock()
	ad.isRunning = true
	ad.mutex.Unlock()
	go ad.audioDecodeThread()

	return ad, nil
}

// AudioDecoderDestroy stops and destroys an audio decoder.
// C: audio_decoder_destroy (audio.c:208-224) — mp_send_cmd(EXIT) to the
// audio queue, join the thread, mq_flush, free resampler + spdif muxer.
func (am *AudioManager) AudioDecoderDestroy(ad *AudioDecoder) {
	if ad == nil {
		return
	}

	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)
	if mp != nil && mp.Audio != nil {
		// C: mp_send_cmd(ad->ad_mp, &ad->ad_mp->mp_audio, MB_CTRL_EXIT)
		mediacore.MpSendCmd(mp, mp.Audio, int(mediacore.MBCtrlExit))
	} else {
		// No media pipe — wake the parked local loop instead
		ad.mutex.Lock()
		ad.isRunning = false
		ad.cancel()
		ad.cond.Broadcast()
		ad.mutex.Unlock()
	}

	// C: hts_thread_join(&ad->ad_tid)
	if ad.threadDone != nil {
		select {
		case <-ad.threadDone:
		case <-time.After(2 * time.Second):
		}
	}

	// C: mq_flush(ad->ad_mp, &ad->ad_mp->mp_audio, 1)
	if mp != nil && mp.Audio != nil {
		mediacore.MqFlush(mp, mp.Audio, true)
	}

	// C: avresample_close + avresample_free(&ad->ad_avr)
	if ad.AVR != nil {
		ad.AVR.Close()
		ad.AVR = nil
	}

	// C: audio_cleanup_spdif_muxer(ad)
	ad.audioCleanupSPDIFMuxer()
}

// mqWaitTimeout waits on mq.Avail for at most ms milliseconds.
// C: hts_cond_wait_timeout(&mq->mq_avail, &mp->mp_mutex, ms) (audio.c:662).
// Callers must hold mp.Mutex.
func mqWaitTimeout(mq *mediacore.MediaQueue, ms int) {
	t := time.AfterFunc(time.Duration(ms)*time.Millisecond, func() {
		mq.Avail.Broadcast()
	})
	mq.Avail.Wait()
	t.Stop()
}

// audioDecodeThread is the main audio processing thread.
// C: audio_decode_thread (audio.c:619-802) — delivers samples, handles
// control buffers, and processes audio data buffers, all under mp_mutex
// with waits on mq->mq_avail.
func (ad *AudioDecoder) audioDecodeThread() {
	defer close(ad.threadDone)
	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)

	// C: if(ac->ac_init != NULL) ac->ac_init(ad)
	if ad.Class != nil && ad.Class.Start != nil {
		ad.Class.Start(ad)
	}

	// C: ad->ad_discontinuity = 1
	ad.Discontinuity = true

	if mp == nil || mp.Audio == nil {
		// No media pipe to drain — C always has one (mp_init_audio);
		// Go-compat: park until destroy.
		ad.mutex.Lock()
		for ad.isRunning {
			ad.cond.Wait()
		}
		ad.mutex.Unlock()
		if ad.Class != nil && ad.Class.Fini != nil {
			ad.Class.Fini(ad)
		}
		return
	}
	mq := mp.Audio
	blocked := false
	run := true

	// C: hts_mutex_lock(&mp->mp_mutex)
	mp.Mutex.Lock()
	for run {
		var avail int
		if ad.SPDIFMuxer != nil {
			avail = ad.SPDIFFrameSize
		} else if ad.AVR != nil {
			avail = ad.AVR.Available()
		}
		hasData := len(mq.DataQueue) > 0
		hasCtrl := len(mq.CtrlQueue) > 0

		// C: if(avail >= ad->ad_tile_size && blocked == 0 &&
		//     !ad->ad_paused && !ctrl)
		if avail >= ad.TileSize && !blocked && !ad.Paused && !hasCtrl {
			samples := min(avail, ad.TileSize)
			// C: the driver's ac_deliver_* pulls `samples` (or fewer,
			// bounded by device availability) from ad_avr /
			// ad_spdif_frame itself inside the call — no pre-read.

			var r int
			if ad.Class.DeliverLocked != nil {
				// C: r = ac->ac_deliver_locked(ad, samples, pts, epoch)
				r = ad.Class.DeliverLocked(ad, samples, ad.PTS, ad.Epoch)
				if r != 0 {
					// C: r==-1 → hts_cond_wait, else wait_timeout(r)
					if r == -1 {
						mq.Avail.Wait()
					} else {
						mqWaitTimeout(mq, r)
					}
					continue
				}
			} else {
				// C: hts_mutex_unlock; r = ac->ac_deliver_unlocked(...);
				//    hts_mutex_lock
				mp.Mutex.Unlock()
				r = ad.Class.DeliverUnlocked(ad, samples, ad.PTS, ad.Epoch)
				mp.Mutex.Lock()
			}

			if r != 0 {
				blocked = true
			} else {
				// C: ad->ad_pts = AV_NOPTS_VALUE
				ad.PTS = mediacore.PTSUnset
			}
			continue
		}

		var mb *mediacore.MediaBuf
		if hasCtrl {
			// C: TAILQ_REMOVE(&mq->mq_q_ctrl, ctrl, mb_link); mb = ctrl
			mb = mq.CtrlQueue[0]
			mq.CtrlQueue = mq.CtrlQueue[1:]
		} else if hasData && avail < ad.TileSize {
			// C: TAILQ_REMOVE(&mq->mq_q_data, data, mb_link);
			//    mp_check_underrun(mp); mb = data
			mb = mq.DataQueue[0]
			mq.DataQueue = mq.DataQueue[1:]
			mediacore.MpCheckUnderrun(mp)
			// C: if(mb->mb_dts != PTS_UNSET) mq->mq_last_deq_dts = mb->mb_dts
			if mb.DTS != mediacore.PTSUnset {
				mq.LastDeqDTS = mb.DTS
			}
		} else {
			// C: hts_cond_wait(&mq->mq_avail, &mp->mp_mutex)
			mq.Avail.Wait()
			continue
		}

		// C: mq->mq_packets_current--;
		//    mp->mp_buffer_current -= mb_buffered_size(mb)
		mq.PacketsCurrent--
		mp.BufferCurrent -= mediacore.MbBufferedSize(mb)

		if mb.DataType == int(mediacore.MBCtrlUnblock) {
			// C: assert(blocked); blocked = 0
			blocked = false
		} else if ad.Mode == ModeCoded &&
			ad.Class.DeliverCodedLocked != nil &&
			mb.DataType == int(mediacore.MBAudio) {
			// C: coded-mode audio delivered under mp_mutex (audio.c:692)
			if !mb.Flags.Skip && mb.Stream == mq.Stream {
				r := ad.Class.DeliverCodedLocked(ad, mb.Data, mb.PTS, mb.Epoch)
				if r != 0 {
					// C: TAILQ_INSERT_HEAD + wait on mq_avail
					mq.DataQueue = slices.Insert(mq.DataQueue, 0, mb)
					mq.PacketsCurrent++
					mp.BufferCurrent += mediacore.MbBufferedSize(mb)
					mq.Avail.Wait()
					continue
				}
				updateABitrate(ad, mp, mq, mb.Size)
			}
		} else {
			// C: hts_mutex_unlock(&mp->mp_mutex) — the switch runs unlocked
			mp.Mutex.Unlock()
			switch mediacore.MediaBufDataType(mb.DataType) {
			case mediacore.MBAudio:
				// C: if(audio_process_audio(ad, mb)) → retain + requeue
				requeue := ad.audioProcessAudio(mb)
				if requeue {
					mp.Mutex.Lock()
					mq.PacketsCurrent++
					mp.BufferCurrent += mediacore.MbBufferedSize(mb)
					mq.DataQueue = slices.Insert(mq.DataQueue, 0, mb)
					continue
				}

			case mediacore.MBSetPropString:
				// C: prop_set_string(mb->mb_prop, mb->mb_data)
				if mb.Prop != nil {
					mb.Prop.SetString(string(mb.Data))
				}

			case mediacore.MBCtrlSetVolumeMultiplier:
				// C: ad->ad_vol_scale = mb->mb_float
				ad.VolScale = mb.Float
				if ad.Class.SetVolume != nil {
					ad.Class.SetVolume(ad, ad.VolScale)
				}

			case mediacore.MBCtrlPause:
				ad.Paused = true
				if ad.Class.Pause != nil {
					ad.Class.Pause(ad)
				}

			case mediacore.MBCtrlPlay:
				ad.Paused = false
				if ad.Class.Play != nil {
					ad.Class.Play(ad)
				}

			case mediacore.MBCtrlFlush:
				// C: audio.c:723-745 — clear error filters, ac_flush,
				// pts=NOPTS, seek_audio_done, discontinuity, drain avr
				ad.ChannelLayoutFail = false
				ad.SampleRateFail = false
				if ad.Class.Flush != nil {
					ad.Class.Flush(ad)
				}
				ad.PTS = mediacore.PTSUnset
				if mp.SeekAudioDone != nil {
					mp.SeekAudioDone(mp)
				}
				ad.Discontinuity = true
				if ad.AVR != nil {
					ad.AVR.Read(nil, ad.AVR.Available())
				}

			case mediacore.MBCtrlExit:
				run = false

			case mediacore.MBCtrlReconfigure:
				if ad.Class.Reconfigure != nil {
					ad.Class.Reconfigure(ad)
				}

			default:
				// C: abort() — unknown control type; ignore in Go
			}
			// C: hts_mutex_lock(&mp->mp_mutex)
			mp.Mutex.Lock()
		}
		// C: mq_update_stats(mp, mq, 1); hts_cond_signal(&mp->mp_backpressure);
		//    media_buf_free_locked(mp, mb)
		mediacore.MqUpdateStats(mp, mq, 1)
		if mp.Backpressure != nil {
			mp.Backpressure.Signal()
		}
		mediacore.MediaBufFreeLocked(mp, mb)
	}
	mp.Mutex.Unlock()

	// C: glw_rec_audio_send(ad, NULL, PTS_UNSET) (audio.c:797) —
	//    compiled only under CONFIG_GLW_REC; see GlwRecAudioSendHook.
	if audioDeps.glwRecSend != nil {
		audioDeps.glwRecSend(ad, nil, 0, mediacore.PTSUnset)
	}

	// C: if(ac->ac_fini != NULL) ac->ac_fini(ad)
	if ad.Class != nil && ad.Class.Fini != nil {
		ad.Class.Fini(ad)
	}
}

// channelLayoutNbChannels — C: av_get_channel_layout_nb_channels
// (libavutil/channel_layout.h).
func channelLayoutNbChannels(layout int64) int {
	n := 0
	for layout != 0 {
		n += int(layout & 1)
		layout >>= 1
	}
	if n == 0 {
		return 2
	}
	return n
}

// audioProcessAudio processes one MB_AUDIO buffer.
// C: audio_process_audio (audio.c:357-613) — single canonical function
// handling codec-change detection, SPDIF/coded/PCM mode switching,
// sample-rate/channel-layout fallback, PTS delay correction, resampler
// reconfiguration, and resample/sleep of decoded frames.
// Returns true if the buffer should be retained (more data to extract).
// Runs with mp_mutex released (the thread unlocks around it); the coded
// branch re-locks internally like C.
func (ad *AudioDecoder) audioProcessAudio(mb *mediacore.MediaBuf) bool {
	mp, _ := ad.MediaPipe.(*mediacore.MediaPipe)
	if mp == nil || mp.Audio == nil {
		return false
	}
	mq := mp.Audio
	ac := ad.Class

	// C: if(mb->mb_skip || mb->mb_stream != mq->mq_stream) return 0
	if mb.Flags.Skip || mb.Stream != mq.Stream {
		return false
	}

	var framePCM []byte // frame->data[0]
	var frameRate int   // frame->sample_rate
	var frameFmt SampleFormat
	var frameLayout int64 // frame->channel_layout
	var frameNb int       // frame->nb_samples
	var r int
	var gotFrame bool

	if mb.Codec == nil {
		// C: raw PCM path (audio.c:376-395)
		frameRate = mb.Rate
		frameFmt = SampleFormatS16
		switch mb.Channels {
		case 1:
			frameLayout = ChannelLayoutMono
			frameNb = mb.Size / 2
		case 2:
			frameLayout = ChannelLayoutStereo
			frameNb = mb.Size / 4
		default:
			return false // C: abort()
		}
		framePCM = mb.Data
		r = mb.Size
		gotFrame = true
		updateABitrate(ad, mp, mq, mb.Size)
	} else {
		// C: media_codec_t *mc = mb->mb_cw; AVCodecContext *ctx = mc->ctx
		mc := mb.Codec
		ctx := mc.Ctx

		if int(mc.CodecID) != ad.InCodecID {
			// C: codec changed (audio.c:404-434)
			ad.InCodecID = int(mc.CodecID)
			ad.InSampleRate = 0

			ad.audioCleanupSPDIFMuxer()

			// C: ad->ad_mode = ac->ac_get_mode != NULL ?
			//   ac->ac_get_mode(...) : AUDIO_MODE_PCM
			ad.Mode = ModePCM
			if ac != nil && ac.GetMode != nil {
				if mode, err := ac.GetMode(ad, int(mc.CodecID), mc.ExtraData); err == nil {
					ad.Mode = mode
				}
			}

			if ad.Mode == ModeSPDIF {
				// C: audio_setup_spdif_muxer + audio_set_passthru_metadata
				ad.setupSPDIFMuxer(int(mc.CodecID))
				ad.setPassthruMetadata(mp, mq, int(mc.CodecID))
			} else if ad.Mode == ModeCoded {
				// C: hts_mutex_lock; audio_set_passthru_metadata;
				//    ac_deliver_coded_locked; hts_mutex_unlock; return 0
				mp.Mutex.Lock()
				ad.setPassthruMetadata(mp, mq, int(mc.CodecID))
				if ac != nil && ac.DeliverCodedLocked != nil {
					ac.DeliverCodedLocked(ad, mb.Data, mb.PTS, mb.Epoch)
				}
				mp.Mutex.Unlock()
				return false
			}
		}

		if ad.SPDIFMuxer != nil {
			// C: audio.c:437-448 — spdif passthrough
			ad.PTS = mb.PTS
			ad.Epoch = mb.Epoch
			updateABitrate(ad, mp, mq, mb.Size)
			mb.PTS = mediacore.PTSUnset
			mb.DTS = mediacore.PTSUnset
			// C: av_write_frame + avio_flush — spdif_mux_write appends
			// to ad->ad_spdif_frame
			if muxed := ad.SPDIFMuxer.Mux(mb.Data); len(muxed) > 0 {
				ad.SPDIFFrame = append(ad.SPDIFFrame, muxed...)
				ad.SPDIFFrameSize = len(ad.SPDIFFrame)
				ad.SPDIFFrameAlloc = cap(ad.SPDIFFrame)
			}
			return false
		}

		if ad.Mode == ModeCoded {
			// C: audio.c:452-456
			ad.PTS = mb.PTS
			ad.Epoch = mb.Epoch
		}

		if ctx == nil {
			// C: audio.c:459-470 — lazy decoder open:
			// avcodec_find_decoder + alloc_context3 +
			// request_channel_layout(stereo downmix) + avcodec_open2
			ctx = medialibav.AudioCodecOpen(int(mc.CodecID), ad.StereoDownmix)
			if ctx == nil {
				return false // C: av_freep(&mc->ctx); return 0
			}
			mc.Ctx = ctx
		}

		// C: r = avcodec_decode_audio4(ctx, frame, &got_frame, &mb->mb_pkt)
		//    if(r < 0) return 0
		var pcm []byte
		var rate, fmtv, nb int
		var layout uint64
		var got bool
		var err error
		if ad.decScratch == nil {
			// generous: 8ch x 8B x 256k samples — allocated once,
			// reused across packets (was: 8MB alloc per packet)
			ad.decScratch = make([]byte, 8*1024*1024)
		}
		if mb.Pkt != nil {
			// Go extension (no C counterpart): decrypt cenc-encrypted
			// packets in place through the codec's DRM decryptor.
			if derr := medialibav.DRMDecryptPacket(mc, mb.Pkt); derr != nil {
				return false
			}
			// C: the packet lives in mb->mb_pkt (av_packet_ref'd)
			pcm, rate, fmtv, layout, _, nb, got, err =
				medialibav.AudioFrameDecodePktInto(ctx, mb.Pkt, ad.decScratch)
		} else {
			pcm, rate, fmtv, layout, _, nb, got, err =
				medialibav.AudioFrameDecodeInto(ctx, mb.Data, mb.PTS, mb.DTS, ad.decScratch)
		}
		if err != nil {
			return false
		}
		// send/receive consumes the whole packet per call
		r = mb.Size
		updateABitrate(ad, mp, mq, r)

		framePCM = pcm
		frameRate = rate
		frameFmt = SampleFormat(fmtv)
		frameLayout = int64(layout)
		frameNb = nb
		gotFrame = got

		if frameRate == 0 {
			// C: audio.c:477-492 — sample rate fallbacks
			frameRate = medialibav.AudioCodecCtxSampleRate(ctx)
			if frameRate == 0 && mc.FmtCtx != nil {
				frameRate = medialibav.AudioCodecCtxSampleRate(mc.FmtCtx)
			}
			if frameRate == 0 {
				if !ad.SampleRateFail {
					ad.SampleRateFail = true
				}
				return false
			}
		}

		if frameLayout == 0 {
			// C: audio.c:495-508 — default channel layout from ctx
			frameLayout = int64(medialibav.AudioDefaultChannelLayout(
				medialibav.AudioCodecCtxChannels(ctx)))
			if frameLayout == 0 {
				if !ad.ChannelLayoutFail {
					ad.ChannelLayoutFail = true
				}
				return false
			}
		}

		// C: mp_set_mq_meta(mq, ctx->codec, ctx) (audio.c:510)
		medialibav.MqSetMeta(mq, ctx)
	}

	if mb.PTS != mediacore.PTSUnset {
		// C: audio.c:514-531 — PTS correction for resampler delay
		var od, id int64
		if ad.AVR != nil && ad.OutSampleRate > 0 && frameRate > 0 {
			od = int64(ad.AVR.Available()) * 1000000 / int64(ad.OutSampleRate)
			id = ad.AVR.GetDelay() * 1000000 / int64(frameRate)
		}
		ad.PTS = mb.PTS - od - id
		ad.Epoch = mb.Epoch

		if mb.DriveClock != 0 {
			// C: mp_set_current_time(mp, mb->mb_user_time,
			//   mb->mb_epoch, ad->ad_delay)
			var pm *propcore.PropManager
			if ad.manager != nil {
				pm = ad.manager.pm
			}
			mediacore.MpSetCurrentTime(mp, pm, mb.UserTime, mb.Epoch,
				int64(ad.Delay))
		}

		mb.PTS = mediacore.PTSUnset // No longer valid
		mb.UserTime = mediacore.PTSUnset
	}

	// C: mb->mb_data += r; mb->mb_size -= r
	if r <= len(mb.Data) {
		mb.Data = mb.Data[r:]
	} else {
		mb.Data = nil
	}
	mb.Size -= r
	if mb.Size < 0 {
		mb.Size = 0
	}

	// C: if(!got_frame) return mb->mb_size > 0
	if !gotFrame {
		return mb.Size > 0
	}

	// C: audio.c:537-597 — input format changed or reconfig requested
	if frameRate != ad.InSampleRate ||
		frameFmt != ad.InSampleFormat ||
		frameLayout != ad.InChannelLayout ||
		ad.WantReconfig {

		ad.WantReconfig = false
		ad.InSampleRate = frameRate
		ad.InSampleFormat = frameFmt
		ad.InChannelLayout = frameLayout

		// C: ac->ac_reconfig(ad)
		if ac != nil && ac.Reconfig != nil {
			ac.Reconfig(ad)
		}

		// C: ad->ad_avr = avresample_alloc_context(); av_opt_set_*;
		//    avresample_open (audio.c:552-593)
		ad.AVR = NewAudioResampler(
			frameRate, ad.OutSampleRate,
			frameFmt, ad.OutSampleFormat,
			channelLayoutNbChannels(frameLayout), channelLayoutNbChannels(ad.OutChannelLayout),
		)

		// C: prop_set(mp->mp_prop_ctrl, "canAdjustVolume", PROP_SET_INT, 1)
		ad.ctrlSetInt(mp, "canAdjustVolume", 1)

		// C: if(ac->ac_set_volume != NULL)
		//    ac->ac_set_volume(ad, ad->ad_vol_scale)
		if ac != nil && ac.SetVolume != nil {
			ac.SetVolume(ad, ad.VolScale)
		}
	}

	// C: ad->ad_estimated_duration =
	//    1000000LL * frame->nb_samples / frame->sample_rate
	if frameRate > 0 {
		ad.EstimatedDuration = 1000000 * frameNb / frameRate
	}

	// C: if(ad->ad_avr != NULL) avresample_convert(...) else usleep(...)
	if ad.AVR != nil {
		ad.AVR.Convert(framePCM, frameNb)
	} else if ad.EstimatedDuration > 0 {
		time.Sleep(time.Duration(ad.EstimatedDuration) * time.Microsecond)
	}

	// C: glw_rec_audio_send(ad, frame, PTS_UNSET) (audio.c:609) —
	//    compiled only under CONFIG_GLW_REC; the glw package registers
	//    the hook only when built with the "glwrec" tag.
	if audioDeps.glwRecSend != nil {
		audioDeps.glwRecSend(ad, framePCM, frameNb, mediacore.PTSUnset)
	}

	// C: return mb->mb_size > 0
	return mb.Size > 0
}

// ctrlSetInt implements C's prop_set(mp->mp_prop_ctrl, name,
// PROP_SET_INT, v) — find-or-create a named child and set it.
func (ad *AudioDecoder) ctrlSetInt(mp *mediacore.MediaPipe, name string, v int) {
	if mp == nil || mp.PropCtrl == nil || ad.manager == nil || ad.manager.pm == nil {
		return
	}
	pm := ad.manager.pm
	p := pm.Find(mp.PropCtrl, name)
	if p == nil {
		p = pm.CreateEx(mp.PropCtrl, name, nil, false, false)
	}
	if p != nil {
		p.SetInt(v)
	}
}

// setPassthruMetadata — C: audio_set_passthru_metadata (audio.c:246-268).
// Writes the "<name> Pass-Through" codec prop, resets input format
// tracking, and clears canAdjustVolume (audio.c:246-268).
func (ad *AudioDecoder) setPassthruMetadata(mp *mediacore.MediaPipe,
	mq *mediacore.MediaQueue, codecID int) {
	var name string
	switch codecID {
	case medialibav.AVCodecIDDts:
		name = "DTS"
	case medialibav.AVCodecIDAc3:
		name = "AC3"
	case medialibav.AVCodecIDEac3:
		name = "EAC3"
	}
	str := name
	if name != "" {
		str += " "
	}
	str += "Pass-Through"
	if mq != nil && mq.PropCodec != nil {
		mq.PropCodec.SetString(str)
	}

	ad.InSampleRate = 0
	ad.InSampleFormat = 0
	ad.InChannelLayout = 0
	ad.ctrlSetInt(mp, "canAdjustVolume", 0)
}

// setupSPDIFMuxer — C: audio_setup_spdif_muxer (audio.c:274-312).
func (ad *AudioDecoder) setupSPDIFMuxer(codecID int) {
	ad.SPDIFMuxer = NewSPDIFMuxer(codecID, 48000)
	ad.MuxBuffer = make([]byte, 16384)
	ad.SPDIFFrame = nil
	ad.SPDIFFrameSize = 0
	ad.SPDIFFrameAlloc = 0
}

// audioCleanupSPDIFMuxer — C: audio_cleanup_spdif_muxer (audio.c:188-203).
func (ad *AudioDecoder) audioCleanupSPDIFMuxer() {
	if ad.SPDIFMuxer == nil {
		return
	}
	ad.SPDIFMuxer.Close()
	ad.SPDIFFrame = nil
	ad.SPDIFFrameSize = 0
	ad.SPDIFFrameAlloc = 0
	ad.MuxBuffer = nil
	ad.SPDIFMuxer = nil
}

// updateABitrate updates audio bitrate estimation.
// C: update_abitrate (audio.c:316-350) — tracks frame sizes in a ring
// buffer, computes bitrate, and sets mq->mq_prop_bitrate.
func updateABitrate(ad *AudioDecoder, mp *mediacore.MediaPipe, mq *mediacore.MediaQueue, size int) {
	ad.FrameSize[ad.FrameSizePtr] = size
	ad.FrameSizePtr = (ad.FrameSizePtr + 1) & 0x0F

	d := ad.EstimatedDuration
	if d == 0 {
		ad.LastPTS = ad.SavedPTS
		ad.SavedPTS = ad.PTS

		if ad.PTS != mediacore.PTSUnset && ad.LastPTS != mediacore.PTSUnset {
			d64 := ad.PTS - ad.LastPTS
			if d64 > 100 && d64 < 500000 {
				d = int(d64)
			}
		}
	}

	if d == 0 || (ad.FrameSizePtr&7) != 0 {
		return
	}

	sum := 0
	for i := range 16 {
		sum += ad.FrameSize[i]
	}

	// C: sum = 8000000LL * sum / AD_FRAME_SIZE_LEN / d
	//    prop_set_int(mq->mq_prop_bitrate, sum / 1000)
	bitrate := int(8000000*int64(sum)/16/int64(d)) / 1000
	ad.Bitrate = bitrate
	if mq != nil && mq.PropBitrate != nil {
		mq.PropBitrate.SetInt(bitrate)
	}
}
