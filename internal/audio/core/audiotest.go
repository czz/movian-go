package core

// audiotest.go — canonical port of src/audio2/audio_test.c
//
// Test signal generator: pink noise / sine / speaker-position voices
// encoded to AC3 and injected into a dedicated media_pipe. Enabled via
// the "Audio test" settings section (audio_test_init).

import (
	"math"
	"math/bits"
	"sync"
	"time"
	"unsafe"

	"github.com/czz/movian-go/internal/event"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

const (
	avSampleFmtFLTP   = 8    // C: AV_SAMPLE_FMT_FLTP
	avChLayout5Point1 = 0x3f // C: AV_CH_LAYOUT_5POINT1
)

// pcm_sound_t
type pcmSound struct {
	data    []int16
	samples int
}

// C file-scope statics
var (
	audiotestSignalType  int         // C: static int signal_type
	audiotestChannels    [8]int      // C: static int test_channels[8]
	audiotestSampleRate  = 48000     // C: static int sample_rate = 48000
	audiotestWhiteSeed   uint32      // C: static uint32_t seed (gen_white_noise)
	audiotestPinkOctaves [16]float32 // C: static float octaves[16]
	audiotestPinkX       float32     // C: static float x

	audiotestMu    sync.Mutex
	audiotestGenMp *mediacore.MediaPipe              // C: static media_pipe_t *gen_mp
	audiotestFAM   *fileaccesscore.FileAccessManager // C: implicit fa context
	audiotestDone  chan struct{}                     // C: static hts_thread_t generator_tid
	audiotestPM    *propcore.PropManager             // prop manager for mp_create/configure
)

// gen_sinewave (audio_test.c:44)
func genSinewave(sample int) float32 {
	a := (sample * 4096 * 440 / audiotestSampleRate) & 0xfff
	return float32(math.Sin(float64(a)*0.00153398)) * 0.25
}

// gen_white_noise (audio_test.c:54) — LCG whose high bits are
// reinterpreted as a float in [1,2), shifted to [-2,1).
func genWhiteNoise(sample int) float32 {
	audiotestWhiteSeed = audiotestWhiteSeed*196314165 + 907633515
	// C: union { uint32_t u32; float f } u;
	//    u.u32 = (seed >> 9) | 0x40000000; return u.f - 3.0f;
	return math.Float32frombits((audiotestWhiteSeed>>9)|0x40000000) - 3.0
}

// gen_pink_noise (audio_test.c:69) — Voss-McCartney octave filter.
func genPinkNoise(sample int) float32 {
	// C: int k = __builtin_ctz(sample) & 0xf
	// (ctz(0) is UB in C; Go's TrailingZeros32(0)=32 → k=0, same as tzcnt)
	k := bits.TrailingZeros32(uint32(sample)) & 0xf
	prev := audiotestPinkOctaves[k]

	for {
		r := genWhiteNoise(0) * 0.5
		audiotestPinkOctaves[k] = r
		r -= prev
		audiotestPinkX += r
		if audiotestPinkX < -4.0 || audiotestPinkX > 4.0 {
			audiotestPinkX -= r
		} else {
			break
		}
	}
	return (genWhiteNoise(0)*0.5 + audiotestPinkX) * 0.125
}

// unpack_audio (audio_test.c:88) — decodes url into interleaved int16
// stereo samples (mono source duplicated into both channels, as C does).
func unpackAudio(url string, out *pcmSound) int {
	fh, err := fileaccesscore.FAOpenEx(audiotestFAM, url, 0, nil)
	if fh == nil {
		audiotestFAM.TraceSystem().Trace(trace.TRACE_ERROR, "audiotest",
			"Unable to open %s -- %s", url, err)
		return -1
	}

	libavSys := libav.GetGlobalLibAVSystem()
	avio, aerr := libav.FALibavReopen(libavSys, fh, false)
	if aerr != nil || avio == nil {
		audiotestFAM.TraceSystem().Trace(trace.TRACE_ERROR, "audiotest",
			"Unable to open %s -- %s", url, aerr)
		return -1
	}

	fctx, ferr := libav.FALibavOpenFormat(avio, url, "",
		libav.FaLibavOpenStrategyAudio)
	if fctx == nil {
		libav.FALibavClose(libavSys, avio)
		audiotestFAM.TraceSystem().Trace(trace.TRACE_ERROR, "audiotest",
			"Unable to open %s -- %s", url, ferr)
		return -1
	}

	var ctx *libav.AVCodecContext
	s := 0
	for ; s < fctx.GetNbStreams(); s++ {
		c := fctx.StreamCodecCtx(s)
		if c == nil {
			continue
		}
		if c.GetCodecType() != libav.AvmediaTypeAudio {
			libav.AvcodecFreeContext(c)
			continue
		}
		codec := libav.AvcodecFindDecoder(c.GetCodecID())
		if codec == nil {
			libav.AvcodecFreeContext(c)
			continue
		}
		if libav.AvcodecOpen2(c, codec, nil) != nil {
			audiotestFAM.TraceSystem().Trace(trace.TRACE_ERROR, "audiotest", "Unable to codec")
			libav.AvcodecFreeContext(c)
			continue
		}
		ctx = c
		break
	}

	frame := libav.AvFrameAlloc()

	out.samples = 0
	out.data = nil

	for {
		pkt := libav.AvPacketAlloc()
		if pkt == nil {
			break
		}
		r := libav.AvReadFrameCode(fctx, pkt)
		if r == libav.AverrorEagain {
			libav.AvPacketFree(pkt)
			continue
		}
		if r != 0 {
			libav.AvPacketFree(pkt)
			break
		}
		_, _, _, streamIndex, _, _ := libav.AvPacketFieldsPtr(pkt.CPtr())
		if streamIndex == s && ctx != nil {
			// C: while(pkt.size) avcodec_decode_audio4(...) consume loop —
			// FFmpeg 7: send whole packet, drain all frames.
			if libav.AvcodecSendPacket(ctx, pkt) == nil {
				for libav.AvcodecReceiveFrame(ctx, frame) == nil {
					n := libav.AvFrameNbSamples(frame.CPtr())
					ns := n * 2
					p := frame.DataPtr(0)
					if p == nil {
						continue
					}
					src := unsafe.Slice((*int16)(p), n)
					base := out.samples
					out.data = append(out.data, make([]int16, ns)...)
					for i := range n {
						v := src[i]
						out.data[base+i*2+0] = v
						out.data[base+i*2+1] = v
					}
					out.samples += ns
				}
			}
		}
		libav.AvPacketFree(pkt)
	}
	libav.AvFrameFree(frame)
	if ctx != nil {
		libav.AvcodecClose(ctx)
		libav.AvcodecFreeContext(ctx)
	}
	libav.FALibavCloseFormat(libavSys, fctx, false)
	return 0
}

// unpack_speaker_positions (audio_test.c:184)
func unpackSpeakerPositions(v *[8]pcmSound) {
	unpackAudio("dataroot://res/speaker_positions/fl.mp3", &v[0])
	unpackAudio("dataroot://res/speaker_positions/fr.mp3", &v[1])
	unpackAudio("dataroot://res/speaker_positions/c.mp3", &v[2])
	unpackAudio("dataroot://res/speaker_positions/lfe.mp3", &v[3])
	unpackAudio("dataroot://res/speaker_positions/sl.mp3", &v[4])
	unpackAudio("dataroot://res/speaker_positions/sr.mp3", &v[5])
	unpackAudio("dataroot://res/speaker_positions/rl.mp3", &v[6])
	unpackAudio("dataroot://res/speaker_positions/rr.mp3", &v[7])
}

// test_generator_thread (audio_test.c:199) — encodes generated samples
// to AC3 and feeds them to the media pipe as MB_AUDIO buffers.
func audiotestGeneratorThread(mp *mediacore.MediaPipe, done chan struct{}) {
	defer close(done)

	var mb *mediacore.MediaBuf
	mq := mp.Audio
	var voices [8]pcmSound

	frame := libav.AvFrameAlloc()
	codec := libav.AvcodecFindEncoder(libav.AVCodecIDAC3)
	ctx := libav.AvcodecAllocContext3(codec)

	ctx.SetSampleFmt(avSampleFmtFLTP)
	ctx.SetSampleRate(48000)
	ctx.SetChannelLayout(avChLayout5Point1)

	if libav.AvcodecOpen2Encoder(ctx, codec, nil) != nil {
		audiotestFAM.TraceSystem().Trace(trace.TRACE_ERROR, "audio", "Unable to open encoder")
		return
	}

	frameSize := ctx.FrameSize()
	var genbuf [8]unsafe.Pointer
	var genbufF [8][]float32
	for i := range 8 {
		genbuf[i] = libav.AvMalloc(frameSize * 4)
		if genbuf[i] != nil {
			genbufF[i] = unsafe.Slice((*float32)(genbuf[i]), frameSize)
		}
		frame.SetData(i, genbuf[i])
	}
	frame.SetNbSamples(frameSize)
	frame.SetFormat(avSampleFmtFLTP)
	frame.SetSampleRate(48000)
	frame.SetChannelLayout(avChLayout5Point1)

	// C: media_codec_create(AV_CODEC_ID_AC3, 0, NULL, NULL, NULL, mp)
	mc := mediacore.MediaCodecCreate(mediacore.CodecIDAC3, 0,
		nil, nil, nil, mp)

	mp.Audio.Stream = 0
	mediacore.MpConfigure(mp, audiotestPM, 0,
		mediacore.MPBufferNone, 0, "testsignal")
	mediacore.MpBecomePrimary(mp, audiotestPM)

	sample := 0

	unpackSpeakerPositions(&voices)

	for {
		if mb == nil {
			var g func(int) float32
			doGen := true

			switch audiotestSignalType {
			case 0:
				for c := range 8 {
					z := frameSize
					if audiotestChannels[c] != 0 {
						j := sample & 0xffff
						toCopy := max(min(voices[c].samples-j, frameSize), 0)
						for i := range toCopy {
							genbufF[c][i] = float32(voices[c].data[j+i]) / 32767.0
						}
						z = frameSize - toCopy
					}
					// C: memset(genbuf[c] + ctx->frame_size - z, 0, ...)
					for i := frameSize - z; i < frameSize; i++ {
						genbufF[c][i] = 0
					}
				}
				sample += frameSize
				doGen = false

			case 2:
				g = genSinewave
			default:
				g = genPinkNoise
			}

			if doGen {
				for i := range frameSize {
					x := g(sample)
					for c := range 8 {
						if audiotestChannels[c] != 0 {
							genbufF[c][i] = x
						} else {
							genbufF[c][i] = 0
						}
					}
					sample++
				}
			}

			// C: encode:
			pkt := libav.AvPacketAlloc()
			gotPacket := false
			if libav.AvcodecSendFrame(ctx, frame) == nil {
				if libav.AvcodecReceivePacket(ctx, pkt) == nil {
					gotPacket = true
				}
			}
			if gotPacket {
				mb = mediacore.MediaBufFromAVPkt(mp, pkt)
				libav.AvPacketFree(pkt)
			} else {
				libav.AvPacketFree(pkt)
				time.Sleep(time.Second)
			}

			// C sets these unconditionally — mb NULL-derefs on encode
			// failure (latent C crash); a Go nil-pointer panic is the
			// equivalent observable behavior.
			mb.Codec = mediacore.MediaCodecRef(mc)
			mb.DataType = int(mediacore.MBAudio)
			mb.PTS = mediacore.PTSUnset
		}

		e := mediacore.MbEnqueueWithEvents(mp, mq, mb)
		if e == nil {
			mb = nil // Enqueue succeeded
			continue
		}

		if e.Type == int(event.EVENT_EXIT) {
			mediacore.MpFlush(mp)
			break
		}
		// C: event_release(e) — Go GC handles it
	}

	libav.AvFrameFree(frame)

	for i := range 8 {
		if genbuf[i] != nil {
			libav.AvFreep(unsafe.Pointer(&genbuf[i]))
		}
	}

	// C: free(voices[i].data) — Go slices are GC'd
	for i := range 8 {
		voices[i].data = nil
	}

	mediacore.MediaCodecDeref(mc)
}

// enable_test_thread (audio_test.c:316)
func audiotestEnableTestThread(on bool) {
	audiotestMu.Lock()
	defer audiotestMu.Unlock()

	// C: if(!generator_tid == !on) return;
	if (audiotestDone == nil) == !on {
		return
	}

	if on {
		// C: assert(gen_mp == NULL)
		audiotestGenMp = mediacore.MpCreate(mediacore.NewMediaSystem(), audiotestPM, "testsignal",
			mediacore.MPPrimaable)
		audiotestDone = make(chan struct{})
		go audiotestGeneratorThread(audiotestGenMp, audiotestDone)
	} else {
		// C: event_create_type(EVENT_EXIT) + mp_enqueue_event + release
		mediacore.MpEnqueueEvent(audiotestGenMp, &mediacore.MediaEvent{
			Type: int(event.EVENT_EXIT),
			Data: &event.Event{Type: event.EVENT_EXIT},
		})
		<-audiotestDone
		mediacore.MpShutdown(audiotestGenMp)
		mediacore.MpDestroy(audiotestGenMp)
		audiotestGenMp = nil
		audiotestDone = nil
	}
}

// enable_set_signal (audio_test.c:345) — SETTING_CALLBACK for the
// "Play test signal" bool setting.
func audiotestEnableSetSignal(_ any, v any) {
	on := false
	switch t := v.(type) {
	case bool:
		on = t
	case int:
		on = t != 0
	case int64:
		on = t != 0
	}
	audiotestEnableTestThread(on)
}

// add_ch_bool (audio_test.c:356)
func audiotestAddChBool(sm *settingscore.SettingsManager,
	title *propcore.Prop, id, def int, asettings *propcore.Prop) {
	sm.SettingCreate(settingscore.SettingBool, asettings,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, title,
		settingscore.SettingTagWriteInt, &audiotestChannels[id],
		settingscore.SettingTagValue, def)
}

// audio_test_init (audio_test.c:372) — registers the "Audio test"
// settings section. Called from AudioStart (C: audio.c:148 under
// CONFIG_AUDIOTEST, enabled by default in this codebase).
func audiotestSetup(am *AudioManager, asettings *propcore.Prop) {
	sm := am.settingsMgr
	if sm == nil || asettings == nil {
		return
	}
	audiotestPM = am.pm
	audiotestFAM = am.fam

	// C: settings_create_separator(asettings, _p("Audio test"))
	sm.CreateSeparatorProp(asettings, sm.P("Audio test"))

	// C: setting_create(SETTING_BOOL, ..., SETTING_TITLE(_p("Play test
	//   signal")), SETTING_CALLBACK(enable_set_signal, NULL), NULL)
	sm.SettingCreate(settingscore.SettingBool, asettings,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Play test signal"),
		settingscore.SettingTagCallback, audiotestEnableSetSignal, nil)

	// C: setting_create(SETTING_MULTIOPT, ..., SETTING_TITLE(_p("Test
	//   signal type")), SETTING_WRITE_INT(&signal_type),
	//   SETTING_OPTION(...) x3, NULL)
	sm.SettingCreate(settingscore.SettingMultiOpt, asettings,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Test signal type"),
		settingscore.SettingTagWriteInt, &audiotestSignalType,
		settingscore.SettingTagOption, "0", sm.P("Speaker position"),
		settingscore.SettingTagOption, "1", sm.P("Pink noise"),
		settingscore.SettingTagOption, "2", sm.P("440Hz sinewave -12dB"))

	audiotestAddChBool(sm, sm.P("Front Left"), 0, 1, asettings)
	audiotestAddChBool(sm, sm.P("Center"), 2, 0, asettings)
	audiotestAddChBool(sm, sm.P("Front Right"), 1, 1, asettings)
	audiotestAddChBool(sm, sm.P("LFE"), 3, 0, asettings)
	audiotestAddChBool(sm, sm.P("Surround Left"), 4, 0, asettings)
	audiotestAddChBool(sm, sm.P("Surround Right"), 5, 0, asettings)
	audiotestAddChBool(sm, sm.P("Rear Left"), 6, 0, asettings)
	audiotestAddChBool(sm, sm.P("Rear Right"), 7, 0, asettings)
}
