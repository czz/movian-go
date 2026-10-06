package media

import (
	"fmt"
	"strings"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	baselibav "github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/media/libav"
	propcore "github.com/czz/movian-go/internal/prop"
)

// Open opens a media URL for playback
func (p *PlaybackPipeline) Open(url string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state != PlaybackStateStopped {
		return fmt.Errorf("playback already active, stop first")
	}

	// Reset cleanup flag and channel for new playback session.
	// stopChan must be cleared to nil so that a subsequent Stop() (before Play())
	// does not attempt to close a stale, already-closed channel from a previous
	// session. Play() creates a fresh stopChan.
	p.cleanedUp = false
	p.stopRequested = false
	p.stopChan = nil
	p.readErrorCount = 0

	// Open the format context. Canonical C path (be_file_playvideo_fh):
	// fa_open → fa AVIO → avformat_open_input(pb). All I/O goes through
	// fileaccess, so https uses Go's TLS — no FFmpeg TLS backend needed.
	// Fall back to a raw URL open only when fileaccess is not wired.
	var fmtCtx *libav.AVFormatCtx
	if p.fam != nil {
		fh, _ := fileaccesscore.FAOpenEx(p.fam, url,
			fileaccesscore.FaBufferedBig, nil)
		if fh != nil {
			libavSys := baselibav.GetGlobalLibAVSystem()
			strategy := baselibav.FALibavGetStrategyForFile(fh)
			avio, err := baselibav.FALibavReopen(libavSys, fh, false)
			if avio == nil {
				fileaccesscore.FAClose(fh)
				return fmt.Errorf("failed to open %s: %w", url, err)
			}
			fc, err := baselibav.FALibavOpenFormat(avio, url, "", strategy)
			if fc == nil {
				baselibav.FALibavClose(libavSys, avio)
				return fmt.Errorf("failed to open %s: %w", url, err)
			}
			p.libavSys = libavSys
			p.fctx = fc
			fmtCtx = libav.WrapFormatCtx(fc)
		}
	}
	if fmtCtx == nil {
		// No fileaccess (or a scheme fa can't handle — pipe:, rtp:…):
		// raw URL open, FFmpeg's own protocol layer.
		var err error
		fmtCtx, err = libav.OpenFormat(url)
		if err != nil {
			return fmt.Errorf("failed to open %s: %w", url, err)
		}
	}

	p.url = url
	p.format = fmtCtx

	// Find best video and audio streams
	p.videoStreamIndex = fmtCtx.FindBestStream(libav.AVMediaTypeVideo)
	p.audioStreamIndex = fmtCtx.FindBestStream(libav.AVMediaTypeAudio)

	// C: mp_create — media pipes are always created through mp_create
	// so queues, conditions, refcount and defaults are initialized.
	p.mediaPipe = mediacore.MpCreate(p.mediaSys, p.propMgr, url, mediacore.MPVideo|mediacore.MPPrimaable)
	p.mediaPipe.Mutex.Lock()
	p.mediaPipe.KVStore = p.kvstore
	p.mediaPipe.Mutex.Unlock()
	p.mediaPipe.FAM = p.fam
	p.mediaPipe.URL = url
	p.mediaPipe.Duration = fmtCtx.GetDuration()

	// Queues were created and MqSetup'ed by MpCreate — bind stream indices.
	p.videoQueue = p.mediaPipe.Video
	p.audioQueue = p.mediaPipe.Audio
	p.videoQueue.Stream = p.videoStreamIndex
	p.audioQueue.Stream = p.audioStreamIndex

	// C: mp->mp_video_frame_deliver — the canonical delivery route used
	// by libav_decode_video (via VideoDeliverFrame). Routes through the
	// decoder's DeliverCallback so the seek-preroll suppression toggle
	// (nil-ing vd.DeliverCallback) keeps working.
	p.mediaPipe.VideoFrameDeliver = func(fi *mediacore.FrameInfo, opaque any) int {
		vd := p.videoDecoder
		if vd == nil || vd.DeliverCallback == nil {
			return -1
		}
		if err := vd.DeliverCallback(fi); err != nil {
			return -1
		}
		return 0
	}

	// Set up property tree nodes
	p.setupProperties()

	// Populate audio/subtitle track lists (mirrors C's media_track.c)
	p.populateTrackLists(fmtCtx)

	// Initialize media clock and frame pacer
	hasAudio := p.audioStreamIndex >= 0
	p.mediaClock = NewMediaClock(hasAudio)
	p.framePacer = NewFramePacer(p.mediaClock)

	// Set up video decoder if video stream exists
	if p.videoStreamIndex >= 0 {
		si := fmtCtx.GetStreamInfo(p.videoStreamIndex)
		if si != nil {
			params := &mediacore.MediaCodecParams{
				CodecID:   mediacore.CodecID(si.CodecID),
				MediaType: mediacore.MediaTypeVideo,
				Width:     si.Width,
				Height:    si.Height,
			}
			// Get extradata if available
			extra := fmtCtx.GetStreamExtradata(p.videoStreamIndex)
			if len(extra) > 0 {
				params.ExtraData = extra
				params.ExtraDataSize = len(extra)
				params.ExtradataSize = len(extra)
			}

			fmt2 := mediacore.MediaFormatCreate(fmtCtx.LibavCtx())
			// C: media_codec_create(codec_id, 0, fw, ctx, mcp, mp) — ctx is
			// the stream's codec context. FFmpeg 7 has no per-stream codec
			// ctx, so materialize it; the registered lavc open copies it.
			sctx := fmtCtx.CodecCtxFromStream(p.videoStreamIndex)
			p.videoCodec = mediacore.MediaCodecCreate(
				mediacore.CodecID(si.CodecID), 0, fmt2, sctx.CPtr(), params, p.mediaPipe)
			libav.FreeCodecCtx(sctx)
			if p.videoCodec == nil {
				return fmt.Errorf("failed to open video codec: no codec for id %d", si.CodecID)
			}
			p.videoCodec.FmtCtx = nil // temp ctx consumed by copy_codec_context
			p.videoCodec.MediaType = mediacore.MediaTypeVideo

			// Note: CopyCodecParams is no longer needed here because
			// MediaCodecCreateLavcWithFormat copies params before avcodec_open2

			p.videoDecoder = &libav.VideoDecoder{
				MP:              p.mediaPipe,
				DeliverCallback: p.DeliverCallback,
				ConvertPixFmt:   libav.AVPixFmtYuv420p,
				ConvertWidth:    si.Width,
				ConvertHeight:   si.Height,
			}
			// Also set up a dynamic callback lookup so that if DeliverCallback
			// is set after Open() (e.g. by the render loop), the decoder uses it.
			// This mirrors C's media_pipe which routes frames to the video widget
			// even if the widget is created after the pipe.
			if p.videoDecoder.DeliverCallback == nil {
				p.videoDecoder.DeliverCallback = func(fi *mediacore.FrameInfo) error {
					// Submit frame to pacer for scheduled display
					if p.framePacer != nil {
						p.framePacer.SubmitFrame(fi)
						// Update AVDiff property (mirrors C's glw_video_common.c:233)
						if p.propAVDiff != nil && fi.PTS > 0 {
							_, avdiffSmoothed, _ := p.framePacer.ComputeAVDiff(fi.PTS, p.epoch)
							p.propMgr.SetFloatEx(p.propAVDiff, nil, float32(avdiffSmoothed))
						}
					}
					// Clear loading state on first frame (mirrors C's video_playback.c:851)
					if p.propLoading != nil {
						p.propMgr.SetIntEx(p.propLoading, nil, 0)
					}
					if p.DeliverCallback != nil {
						return p.DeliverCallback(fi)
					}
					return nil
				}
			}

			// Create h264_mp4toannexb bitstream filter for H.264 in MP4/MOV containers.
			// MP4 stores H.264 in AVCC format (length-prefixed NAL units), but
			// libavcodec's h264 decoder expects Annex B (start code prefixed).
			// C Movian uses av_bitstream_filter_filter() for this conversion.
			fmtName := fmtCtx.GetFormatName()
			codecID := mediacore.CodecID(si.CodecID)
			if (codecID == 27 || codecID == mediacore.CodecID(27)) && // AV_CODEC_ID_H264
				(strings.Contains(fmtName, "mp4") || strings.Contains(fmtName, "mov") ||
					strings.Contains(fmtName, "m4v") || strings.Contains(fmtName, "ipod")) {
				bsf, err := baselibav.NewBSF("h264_mp4toannexb", p.videoCodec.Ctx.CPtr())
				if err == nil {
					p.videoDecoder.BSF = bsf
					p.ts.Debug("PLAYBACK", "Created h264_mp4toannexb BSF for %s container", fmtName)
				} else {
					p.ts.Error("PLAYBACK", "WARNING: failed to create h264_mp4toannexb BSF: %v", err)
				}
			}
		}
	}

	// Set up audio decoder if audio stream exists
	if p.audioStreamIndex >= 0 {
		si := fmtCtx.GetStreamInfo(p.audioStreamIndex)
		if si != nil {
			params := &mediacore.MediaCodecParams{
				CodecID:    mediacore.CodecID(si.CodecID),
				MediaType:  mediacore.MediaTypeAudio,
				SampleRate: si.SampleRate,
				Channels:   si.Channels,
			}
			extra := fmtCtx.GetStreamExtradata(p.audioStreamIndex)
			if len(extra) > 0 {
				params.ExtraData = extra
				params.ExtraDataSize = len(extra)
				params.ExtradataSize = len(extra)
			}

			fmt2 := mediacore.MediaFormatCreate(fmtCtx.LibavCtx())
			sctx := fmtCtx.CodecCtxFromStream(p.audioStreamIndex)
			p.audioCodec = mediacore.MediaCodecCreate(
				mediacore.CodecID(si.CodecID), 0, fmt2, sctx.CPtr(), params, p.mediaPipe)
			libav.FreeCodecCtx(sctx)
			if p.audioCodec == nil {
				return fmt.Errorf("failed to open audio codec: no codec for id %d", si.CodecID)
			}
			p.audioCodec.FmtCtx = nil // temp ctx consumed by copy_codec_context
			p.audioCodec.MediaType = mediacore.MediaTypeAudio
			// Note: CopyCodecParams not needed — MediaCodecCreateLavcWithFormat
			// copies params before avcodec_open2

			ad, err := libav.NewLibAVAudio(p.audioCodec, p.audioQueue, p.mediaPipe)
			if err != nil {
				return fmt.Errorf("failed to init audio decoder: %w", err)
			}
			libav.LibAVAudioSetSampleRate(ad, 48000)
			libav.LibAVAudioSetChannels(ad, 2)
			p.audioDecoder = ad
		}
	}

	return nil
}

// setupProperties wires the playback pipeline to the existing media property tree
// wires the playback pipeline to the existing media property tree
// created by glw_props.go. The props use C-compatible names that skins expect:
//   - media.current.playstatus (values: "play"/"pause"/"stop")
//   - media.current.currenttime (int seconds)
//   - media.current.metadata.duration (int seconds)
//   - media.current.canPause, canSeek, canSkipBackward, canSkipForward
func (p *PlaybackPipeline) setupProperties() {
	if p.propMgr == nil {
		return
	}

	global := p.propMgr.GetGlobal()

	// Find the existing media prop tree (created by glw_props.go at startup)
	p.propMediaRoot = p.propMgr.Find(global, "media")
	if p.propMediaRoot == nil {
		p.propMediaRoot = p.propMgr.CreateEx(global, "media", nil, false, false)
	}

	p.propCurrent = p.propMgr.Find(p.propMediaRoot, "current")
	if p.propCurrent == nil {
		p.propCurrent = p.propMgr.CreateEx(p.propMediaRoot, "current", nil, false, false)
	}

	p.propPlayStatus = p.propMgr.Find(p.propCurrent, "playstatus")
	if p.propPlayStatus == nil {
		p.propPlayStatus = p.propMgr.CreateEx(p.propCurrent, "playstatus", nil, false, false)
	}

	p.propCurrentTime = p.propMgr.Find(p.propCurrent, "currenttime")
	if p.propCurrentTime == nil {
		p.propCurrentTime = p.propMgr.CreateEx(p.propCurrent, "currenttime", nil, false, false)
	}

	p.propCanPause = p.propMgr.Find(p.propCurrent, "canPause")
	if p.propCanPause == nil {
		p.propCanPause = p.propMgr.CreateEx(p.propCurrent, "canPause", nil, false, false)
	}
	p.propCanSeek = p.propMgr.Find(p.propCurrent, "canSeek")
	if p.propCanSeek == nil {
		p.propCanSeek = p.propMgr.CreateEx(p.propCurrent, "canSeek", nil, false, false)
	}
	p.propCanSkipBack = p.propMgr.Find(p.propCurrent, "canSkipBackward")
	if p.propCanSkipBack == nil {
		p.propCanSkipBack = p.propMgr.CreateEx(p.propCurrent, "canSkipBackward", nil, false, false)
	}
	p.propCanSkipFwd = p.propMgr.Find(p.propCurrent, "canSkipForward")
	if p.propCanSkipFwd == nil {
		p.propCanSkipFwd = p.propMgr.CreateEx(p.propCurrent, "canSkipForward", nil, false, false)
	}

	p.propLoading = p.propMgr.Find(p.propCurrent, "loading")
	if p.propLoading == nil {
		p.propLoading = p.propMgr.CreateEx(p.propCurrent, "loading", nil, false, false)
	}

	metadataProp := p.propMgr.Find(p.propCurrent, "metadata")
	if metadataProp == nil {
		metadataProp = p.propMgr.CreateEx(p.propCurrent, "metadata", nil, false, false)
	}
	p.propDuration = p.propMgr.Find(metadataProp, "duration")
	if p.propDuration == nil {
		p.propDuration = p.propMgr.CreateEx(metadataProp, "duration", nil, false, false)
	}
	p.propMetadataTitle = p.propMgr.Find(metadataProp, "title")
	if p.propMetadataTitle == nil {
		p.propMetadataTitle = p.propMgr.CreateEx(metadataProp, "title", nil, false, false)
	}

	// Set initial values using C-compatible semantics
	// This mirrors C's metadata_to_proptree() which sets title, artist, album,
	// format, and duration on the metadata property node.
	if p.format != nil {
		durationSec := p.format.GetDuration() / 1000000
		p.propMgr.SetIntEx(p.propDuration, nil, int(durationSec))

		// Extract metadata from format context (mirrors C's fa_lavf_load_meta)
		md := p.format.GetMetadata()

		// Set title (mirrors C's prop_set(proproot, "title", ...))
		if title, ok := md["title"]; ok && title != "" && p.propMetadataTitle != nil {
			p.propMgr.SetStringEx(p.propMetadataTitle, nil, title, 0)
		}

		// Set artist (mirrors C's prop_set(proproot, "artist", ...))
		// C also tries "author" as fallback
		artist := ""
		if a, ok := md["artist"]; ok && a != "" {
			artist = a
		} else if a, ok := md["author"]; ok && a != "" {
			artist = a
		}
		if artist != "" {
			propArtist := p.propMgr.Find(metadataProp, "artist")
			if propArtist == nil {
				propArtist = p.propMgr.CreateEx(metadataProp, "artist", nil, false, false)
			}
			p.propMgr.SetStringEx(propArtist, nil, artist, 0)
		}

		// Set album (mirrors C's prop_set(proproot, "album", ...))
		if album, ok := md["album"]; ok && album != "" {
			propAlbum := p.propMgr.Find(metadataProp, "album")
			if propAlbum == nil {
				propAlbum = p.propMgr.CreateEx(metadataProp, "album", nil, false, false)
			}
			p.propMgr.SetStringEx(propAlbum, nil, album, 0)
		}

		// Set format (mirrors C's prop_set(proproot, "format", ...))
		if formatName := p.format.GetFormatName(); formatName != "" {
			propFormat := p.propMgr.Find(metadataProp, "format")
			if propFormat == nil {
				propFormat = p.propMgr.CreateEx(metadataProp, "format", nil, false, false)
			}
			p.propMgr.SetStringEx(propFormat, nil, formatName, 0)
		}

		// Set genre if available
		if genre, ok := md["genre"]; ok && genre != "" {
			propGenre := p.propMgr.Find(metadataProp, "genre")
			if propGenre == nil {
				propGenre = p.propMgr.CreateEx(metadataProp, "genre", nil, false, false)
			}
			p.propMgr.SetStringEx(propGenre, nil, genre, 0)
		}

		// Set date if available
		if date, ok := md["date"]; ok && date != "" {
			propDate := p.propMgr.Find(metadataProp, "date")
			if propDate == nil {
				propDate = p.propMgr.CreateEx(metadataProp, "date", nil, false, false)
			}
			p.propMgr.SetStringEx(propDate, nil, date, 0)
		}
	}
	p.propMgr.SetStringEx(p.propPlayStatus, nil, "stop", 0)
	p.propMgr.SetIntEx(p.propCurrentTime, nil, 0)
	p.propMgr.SetIntEx(p.propCanPause, nil, 1)
	p.propMgr.SetIntEx(p.propCanSeek, nil, 1)
	p.propMgr.SetIntEx(p.propCanSkipBack, nil, 1)
	p.propMgr.SetIntEx(p.propCanSkipFwd, nil, 1)
	p.propMgr.SetIntEx(p.propLoading, nil, 0)

	// Create and initialize additional C-compatible media properties (GAP-3).
	// These mirror C's media.c lines 143-269 property creation.
	p.propURL = p.propMgr.Find(p.propCurrent, "url")
	if p.propURL == nil {
		p.propURL = p.propMgr.CreateEx(p.propCurrent, "url", nil, false, false)
	}
	if p.url != "" {
		p.propMgr.SetStringEx(p.propURL, nil, p.url, 0)
	}

	p.propFPS = p.propMgr.Find(p.propCurrent, "fps")
	if p.propFPS == nil {
		p.propFPS = p.propMgr.CreateEx(p.propCurrent, "fps", nil, false, false)
	}
	p.propMgr.SetFloatEx(p.propFPS, nil, 0)

	p.propPauseReason = p.propMgr.Find(p.propCurrent, "pausereason")
	if p.propPauseReason == nil {
		p.propPauseReason = p.propMgr.CreateEx(p.propCurrent, "pausereason", nil, false, false)
	}

	p.propAVDelta = p.propMgr.Find(p.propCurrent, "avdelta")
	if p.propAVDelta == nil {
		p.propAVDelta = p.propMgr.CreateEx(p.propCurrent, "avdelta", nil, false, false)
	}
	p.propMgr.SetIntEx(p.propAVDelta, nil, 0)

	p.propAVDiff = p.propMgr.Find(p.propCurrent, "avdiff")
	if p.propAVDiff == nil {
		p.propAVDiff = p.propMgr.CreateEx(p.propCurrent, "avdiff", nil, false, false)
	}
	p.propMgr.SetFloatEx(p.propAVDiff, nil, 0)

	// Capability flags
	p.propCanEject = p.propMgr.Find(p.propCurrent, "canEject")
	if p.propCanEject == nil {
		p.propCanEject = p.propMgr.CreateEx(p.propCurrent, "canEject", nil, false, false)
	}
	p.propMgr.SetIntEx(p.propCanEject, nil, 0)

	p.propCanStop = p.propMgr.Find(p.propCurrent, "canStop")
	if p.propCanStop == nil {
		p.propCanStop = p.propMgr.CreateEx(p.propCurrent, "canStop", nil, false, false)
	}
	p.propMgr.SetIntEx(p.propCanStop, nil, 1)

	p.propCanShuffle = p.propMgr.Find(p.propCurrent, "canShuffle")
	if p.propCanShuffle == nil {
		p.propCanShuffle = p.propMgr.CreateEx(p.propCurrent, "canShuffle", nil, false, false)
	}
	p.propMgr.SetIntEx(p.propCanShuffle, nil, 0)

	p.propCanRepeat = p.propMgr.Find(p.propCurrent, "canRepeat")
	if p.propCanRepeat == nil {
		p.propCanRepeat = p.propMgr.CreateEx(p.propCurrent, "canRepeat", nil, false, false)
	}
	p.propMgr.SetIntEx(p.propCanRepeat, nil, 0)

	// Playlist state
	p.propShuffle = p.propMgr.Find(p.propCurrent, "shuffle")
	if p.propShuffle == nil {
		p.propShuffle = p.propMgr.CreateEx(p.propCurrent, "shuffle", nil, false, false)
	}
	p.propMgr.SetIntEx(p.propShuffle, nil, 0)

	p.propRepeat = p.propMgr.Find(p.propCurrent, "repeat")
	if p.propRepeat == nil {
		p.propRepeat = p.propMgr.CreateEx(p.propCurrent, "repeat", nil, false, false)
	}
	p.propMgr.SetIntEx(p.propRepeat, nil, 0)

	// Audio track list container
	// C: mp->mp_prop_audio_tracks = prop_create(mp->mp_prop_metadata, "audiostreams")
	p.propAudioTracks = p.propMgr.Find(metadataProp, "audiostreams")
	if p.propAudioTracks == nil {
		p.propAudioTracks = p.propMgr.CreateEx(metadataProp, "audiostreams", nil, false, false)
	}

	// Subtitle track list container
	// C: mp->mp_prop_subtitle_tracks = prop_create(mp->mp_prop_metadata, "subtitlestreams")
	p.propSubTracks = p.propMgr.Find(metadataProp, "subtitlestreams")
	if p.propSubTracks == nil {
		p.propSubTracks = p.propMgr.CreateEx(metadataProp, "subtitlestreams", nil, false, false)
	}

	// Buffer monitoring
	// C: p = prop_create(mp->mp_prop_root, "buffer");
	//     mp->mp_prop_buffer_current = prop_create(p, "current");
	//     mp->mp_prop_buffer_limit = prop_create(p, "limit");
	//     mp->mp_prop_buffer_delay = prop_create(p, "delay");
	p.propBuffer = p.propMgr.Find(p.propCurrent, "buffer")
	if p.propBuffer == nil {
		p.propBuffer = p.propMgr.CreateEx(p.propCurrent, "buffer", nil, false, false)
	}
	p.propBufferCurrent = p.propMgr.Find(p.propBuffer, "current")
	if p.propBufferCurrent == nil {
		p.propBufferCurrent = p.propMgr.CreateEx(p.propBuffer, "current", nil, false, false)
	}
	p.propMgr.SetIntEx(p.propBufferCurrent, nil, 0)

	p.propBufferLimit = p.propMgr.Find(p.propBuffer, "limit")
	if p.propBufferLimit == nil {
		p.propBufferLimit = p.propMgr.CreateEx(p.propBuffer, "limit", nil, false, false)
	}
	p.propMgr.SetIntEx(p.propBufferLimit, nil, 0)

	p.propBufferDelay = p.propMgr.Find(p.propBuffer, "delay")
	if p.propBufferDelay == nil {
		p.propBufferDelay = p.propMgr.CreateEx(p.propBuffer, "delay", nil, false, false)
	}

	// Video container
	p.propVideo = p.propMgr.Find(p.propCurrent, "video")
	if p.propVideo == nil {
		p.propVideo = p.propMgr.CreateEx(p.propCurrent, "video", nil, false, false)
	}

	// Audio container (mirrors C's media.c:165-170)
	propAudio := p.propMgr.Find(p.propCurrent, "audio")
	if propAudio == nil {
		propAudio = p.propMgr.CreateEx(p.propCurrent, "audio", nil, false, false)
	}
	// audio.current (mirrors C's media.c:168)
	propAudioCurrent := p.propMgr.Find(propAudio, "current")
	if propAudioCurrent == nil {
		propAudioCurrent = p.propMgr.CreateEx(propAudio, "current", nil, false, false)
	}
	p.propMgr.SetStringEx(propAudioCurrent, nil, "audio:off", 0)

	// Subtitle container (mirrors C's media.c:188-191)
	propSubtitle := p.propMgr.Find(p.propCurrent, "subtitle")
	if propSubtitle == nil {
		propSubtitle = p.propMgr.CreateEx(p.propCurrent, "subtitle", nil, false, false)
	}
	// subtitle.current (mirrors C's media.c:190)
	propSubCurrent := p.propMgr.Find(propSubtitle, "current")
	if propSubCurrent == nil {
		propSubCurrent = p.propMgr.CreateEx(propSubtitle, "current", nil, false, false)
	}
	p.propMgr.SetStringEx(propSubCurrent, nil, "sub:off", 0)
	p.propSubCurrent = propSubCurrent

	// SUB-1 Selection Wiring:
	// Subscribe to subtitle/current so that when the user selects a track,
	// the external subtitle is automatically loaded.
	//
	// C: mp_track_mgr_init → mtm_current_sub = prop_subscribe(
	//       PROP_TAG_CALLBACK_STRING, mtm_set_current, mtm,
	//       PROP_TAG_ROOT, current)
	//    → mp_enqueue_event_locked → EVENT_SELECT_SUBTITLE_TRACK
	//    → mp_track_mgr_select_track → mp_load_ext_sub
	//
	// Go: propSubCurrent.Subscribe → callback → onSubtitleTrackSelected
	//     → EVENT_SELECT_SUBTITLE_TRACK via MpEnqueueEvent
	p.subCurrentSub = propSubCurrent.Subscribe(
		func(opaque any, eventType propcore.EventType, args ...any) {
			pipeline := opaque.(*PlaybackPipeline)
			if eventType != propcore.EventSetRString && eventType != propcore.EventSetCString {
				return
			}
			// Get the new track ID (URL or "sub:off" or "libav:N")
			trackID := ""
			if len(args) > 0 {
				if s, ok := args[0].(string); ok {
					trackID = s
				}
			}
			pipeline.onSubtitleTrackSelected(trackID)
		},
		p,
		propcore.SubNoInitialUpdate, // don't fire for initial "sub:off"
	)

	// svsdelta (mirrors C's media.c:233)
	propSVDelta := p.propMgr.Find(p.propCurrent, "svdelta")
	if propSVDelta == nil {
		propSVDelta = p.propMgr.CreateEx(p.propCurrent, "svdelta", nil, false, false)
	}
	p.propMgr.SetFloatEx(propSVDelta, nil, 0)

	// avdiffError (mirrors C's media.c:242)
	propAVDiffError := p.propMgr.Find(p.propCurrent, "avdiffError")
	if propAVDiffError == nil {
		propAVDiffError = p.propMgr.CreateEx(p.propCurrent, "avdiffError", nil, false, false)
	}
	p.propMgr.SetFloatEx(propAVDiffError, nil, 0)

	// Video queue stats (mirrors C's media_queue.c:368-380)
	if p.propVideo != nil {
		for _, statName := range []string{"dqlen", "dqmax", "bitrate", "codec", "too_slow"} {
			existing := p.propMgr.Find(p.propVideo, statName)
			if existing == nil {
				p.propMgr.CreateEx(p.propVideo, statName, nil, false, false)
			}
		}
	}

	// Settings subtree (mirrors C's media_settings.c)
	// Video settings
	propVideoSettings := p.propMgr.Find(p.propVideo, "settings")
	if propVideoSettings == nil {
		propVideoSettings = p.propMgr.CreateEx(p.propVideo, "settings", nil, false, false)
	}
	for _, settingName := range []string{"vzoom", "panhorizontal", "panvertical",
		"scalehorizontal", "scalevertical", "hstretch", "fstretch", "vinterpolate"} {
		existing := p.propMgr.Find(propVideoSettings, settingName)
		if existing == nil {
			p.propMgr.CreateEx(propVideoSettings, settingName, nil, false, false)
		}
	}

	// Audio settings
	propAudioSettings := p.propMgr.Find(propAudio, "settings")
	if propAudioSettings == nil {
		propAudioSettings = p.propMgr.CreateEx(propAudio, "settings", nil, false, false)
	}
	for _, settingName := range []string{"audiovolume", "canAdjustVolume", "avdelta"} {
		existing := p.propMgr.Find(propAudioSettings, settingName)
		if existing == nil {
			p.propMgr.CreateEx(propAudioSettings, settingName, nil, false, false)
		}
	}

	// Subtitle settings
	propSubSettings := p.propMgr.Find(propSubtitle, "settings")
	if propSubSettings == nil {
		propSubSettings = p.propMgr.CreateEx(propSubtitle, "settings", nil, false, false)
	}
	for _, settingName := range []string{"svdelta", "subscale", "subalign",
		"subvdisplace", "subhdisplace"} {
		existing := p.propMgr.Find(propSubSettings, settingName)
		if existing == nil {
			p.propMgr.CreateEx(propSubSettings, settingName, nil, false, false)
		}
	}

	// GAP-001: Missing C-compatible media props
	// C: media.c:mp_create creates these props under mp->mp_prop_root (= $global.media.current)

	// primary — C: mp->mp_prop_primary = prop_create(mp->mp_prop_root, "primary")
	p.propPrimary = p.propMgr.Find(p.propCurrent, "primary")
	if p.propPrimary == nil {
		p.propPrimary = p.propMgr.CreateEx(p.propCurrent, "primary", nil, false, false)
	}

	// io — C: mp->mp_prop_io = prop_create(mp->mp_prop_root, "io")
	//   Children: bitrate, bitrateValid, infoNodes (set later during playback)
	p.propIO = p.propMgr.Find(p.propCurrent, "io")
	if p.propIO == nil {
		p.propIO = p.propMgr.CreateEx(p.propCurrent, "io", nil, false, false)
	}

	// notifications — C: mp->mp_prop_notifications = prop_create(mp->mp_prop_root, "notifications")
	p.propNotifications = p.propMgr.Find(p.propCurrent, "notifications")
	if p.propNotifications == nil {
		p.propNotifications = p.propMgr.CreateEx(p.propCurrent, "notifications", nil, false, false)
	}

	// ctrl — C: mp->mp_prop_ctrl = prop_create(mp->mp_prop_root, "ctrl")
	//   Skin references: $self.media.ctrl.audiovolume
	//   Children created by media_settings.c: audiovolume, canAdjustVolume, avdelta,
	//   vzoom, panhorizontal, panvertical, scalehorizontal, scalevertical, etc.
	p.propCtrl = p.propMgr.Find(p.propCurrent, "ctrl")
	if p.propCtrl == nil {
		p.propCtrl = p.propMgr.CreateEx(p.propCurrent, "ctrl", nil, false, false)
	}
	// Create key ctrl sub-props referenced by skin
	for _, ctrlChild := range []string{"audiovolume", "canAdjustVolume"} {
		existing := p.propMgr.Find(p.propCtrl, ctrlChild)
		if existing == nil {
			p.propMgr.CreateEx(p.propCtrl, ctrlChild, nil, false, false)
		}
	}

	// model — C: mp->mp_prop_model = prop_create(mp->mp_prop_root, "model")
	p.propModel = p.propMgr.Find(p.propCurrent, "model")
	if p.propModel == nil {
		p.propModel = p.propMgr.CreateEx(p.propCurrent, "model", nil, false, false)
	}

	// eventSink — C: prop_subscribe with PROP_TAG_NAME("media", "eventSink")
	//   Already created by media.go:MediaSystem.Start as a child of propRoot.
	//   But C also creates it under mp->mp_prop_root for per-media event dispatch.
	p.propEventSink = p.propMgr.Find(p.propCurrent, "eventSink")
	if p.propEventSink == nil {
		p.propEventSink = p.propMgr.CreateEx(p.propCurrent, "eventSink", nil, false, false)
	}

	// seektime — C: media_event.c:142 prop_set(mp->mp_prop_root, "seektime", PROP_SET_FLOAT, ...)
	p.propSeekTime = p.propMgr.Find(p.propCurrent, "seektime")
	if p.propSeekTime == nil {
		p.propSeekTime = p.propMgr.CreateEx(p.propCurrent, "seektime", nil, false, false)
	}

	// svdelta — C: mp->mp_prop_svdelta = prop_create(mp->mp_prop_root, "svdelta")
	p.propSVDelta = p.propMgr.Find(p.propCurrent, "svdelta")
	if p.propSVDelta == nil {
		p.propSVDelta = p.propMgr.CreateEx(p.propCurrent, "svdelta", nil, false, false)
	}
	p.propMgr.SetFloatEx(p.propSVDelta, nil, 0)

	// avdiffError — C: mp->mp_prop_avdiff_error = prop_create(mp->mp_prop_root, "avdiffError")
	p.propAVDiffError = p.propMgr.Find(p.propCurrent, "avdiffError")
	if p.propAVDiffError == nil {
		p.propAVDiffError = p.propMgr.CreateEx(p.propCurrent, "avdiffError", nil, false, false)
	}

	// settings (root level) — C: mp->mp_setting_root = prop_create(mp->mp_prop_root, "settings")
	p.propSettingRoot = p.propMgr.Find(p.propCurrent, "settings")
	if p.propSettingRoot == nil {
		p.propSettingRoot = p.propMgr.CreateEx(p.propCurrent, "settings", nil, false, false)
	}
}

// populateTrackLists creates child properties for each audio and subtitle stream.
// This mirrors C's media.c:171-205 which creates audiostreams/subtitlestreams
// containers and populates them with track entries.
func (p *PlaybackPipeline) populateTrackLists(fmtCtx *libav.AVFormatCtx) {
	if p.propMgr == nil || fmtCtx == nil {
		return
	}

	// Populate audio streams
	if p.propAudioTracks != nil {
		for i := range fmtCtx.GetNumStreams() {
			si := fmtCtx.GetStreamInfo(i)
			if si == nil || si.CodecType != int(libav.AVMediaTypeAudio) {
				continue
			}
			trackName := fmt.Sprintf("audio:%d", i)
			trackProp := p.propMgr.Find(p.propAudioTracks, trackName)
			if trackProp == nil {
				trackProp = p.propMgr.CreateEx(p.propAudioTracks, trackName, nil, false, false)
			}
			// Set track info (mirrors C's media_track_set)
			p.propMgr.SetStringEx(trackProp, nil, fmt.Sprintf("codec:%d", si.CodecID), 0)
		}
	}

	// Populate subtitle streams
	if p.propSubTracks != nil {
		for i := range fmtCtx.GetNumStreams() {
			si := fmtCtx.GetStreamInfo(i)
			if si == nil || si.CodecType != int(libav.AVMediaTypeSubtitle) {
				continue
			}
			trackName := fmt.Sprintf("subtitle:%d", i)
			trackProp := p.propMgr.Find(p.propSubTracks, trackName)
			if trackProp == nil {
				trackProp = p.propMgr.CreateEx(p.propSubTracks, trackName, nil, false, false)
			}
			p.propMgr.SetStringEx(trackProp, nil, fmt.Sprintf("codec:%d", si.CodecID), 0)
		}
	}

	// Set buffer limit (mirrors C's media.c:215 mp_configure setting buffer_limit)
	if p.propBufferLimit != nil {
		p.propMgr.SetIntEx(p.propBufferLimit, nil, 30) // 30 seconds default
	}
}

// Play starts playback
func (p *PlaybackPipeline) Play() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.format == nil {
		return fmt.Errorf("no media opened")
	}

	if p.state == PlaybackStatePlaying {
		return nil
	}

	// If decoders were cleaned up by partialCleanup (after EOF),
	// rebuild them before starting playback.
	if p.format != nil && p.audioDecoder == nil && p.audioStreamIndex >= 0 {
		if err := p.rebuildAudioDecoder(); err != nil {
			return fmt.Errorf("Play: failed to rebuild audio decoder: %w", err)
		}
	}
	if p.format != nil && p.videoCodec == nil && p.videoStreamIndex >= 0 {
		if err := p.rebuildVideoCodec(); err != nil {
			return fmt.Errorf("Play: failed to rebuild video codec: %w", err)
		}
	}

	p.state = PlaybackStatePlaying
	p.stopRequested = false
	p.stopChan = make(chan struct{})

	if p.propPlayStatus != nil {
		p.propMgr.SetStringEx(p.propPlayStatus, nil, "play", 0)
	}

	// Set loading state (mirrors C's video_playback.c:193)
	if p.propLoading != nil {
		p.propMgr.SetIntEx(p.propLoading, nil, 1)
	}

	// Start the packet reader goroutine
	p.wg.Add(1)
	go p.packetLoop()

	return nil
}

// Pause pauses playback
func (p *PlaybackPipeline) Pause() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state != PlaybackStatePlaying {
		return nil
	}

	p.state = PlaybackStatePaused
	if p.propPlayStatus != nil {
		p.propMgr.SetStringEx(p.propPlayStatus, nil, "pause", 0)
	}
	if p.mediaClock != nil {
		p.mediaClock.Pause()
	}
	return nil
}

// Resume resumes playback
func (p *PlaybackPipeline) Resume() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state != PlaybackStatePaused {
		return nil
	}

	p.state = PlaybackStatePlaying
	if p.propPlayStatus != nil {
		p.propMgr.SetStringEx(p.propPlayStatus, nil, "play", 0)
	}
	if p.mediaClock != nil {
		p.mediaClock.Resume()
	}
	return nil
}

// Stop stops playback and cleans up.
// Stop is safe from ANY lifecycle state:
//   - Fresh pipeline (never opened): no-op
//   - After Open() but before Play(): cleans up allocated resources, no channel close
//   - During playback (Playing/Paused): signals packetLoop via stopChan, waits, cleans up
//   - After Stop() or EOF (already cleaned): no-op (idempotent)
//
// The key invariant: stopChan is only closed when playback is active (Playing/Paused),
// because stopChan is only created by Play() and only read by packetLoop.
// Closing stopChan when packetLoop is not running would either panic (nil channel)
// or be a no-op on an already-closed channel.
func (p *PlaybackPipeline) Stop() error {
	p.mu.Lock()

	// Already stopped and cleaned up — nothing to do (idempotent)
	if p.state == PlaybackStateStopped && p.cleanedUp {
		p.mu.Unlock()
		return nil
	}

	// Determine if packetLoop is actually running (needs signaling + waiting)
	wasActive := p.state == PlaybackStatePlaying || p.state == PlaybackStatePaused

	p.state = PlaybackStateStopped
	p.stopRequested = true

	// Only close stopChan if playback was active — stopChan exists and packetLoop
	// is reading from it. Closing nil or already-closed channels would panic.
	if wasActive && p.stopChan != nil {
		close(p.stopChan)
	}

	p.mu.Unlock()

	// Wait for packetLoop to finish (only if it was running)
	if wasActive {
		p.wg.Wait()
	}

	// Clean up all resources (format, decoders, codecs, ALSA, frame pacer).
	// cleanupResources is idempotent via the cleanedUp flag.
	// ALSA is closed here AFTER packetLoop has exited — safe.
	p.cleanupResources()

	return nil
}

// cleanupResources releases all resources associated with the current playback
// session. It is idempotent — safe to call from both Stop() and handleEOF().
// Must NOT be called while packetLoop is still running (caller must ensure
// via wg.Wait() or that packetLoop has already exited via EOF).
func (p *PlaybackPipeline) cleanupResources() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cleanedUp {
		return
	}
	p.cleanedUp = true

	if p.fctx != nil {
		// fa_libav path: closes format + AVIO + the fa handle in one go.
		baselibav.FALibavCloseFormat(p.libavSys, p.fctx, false)
		p.fctx = nil
		p.libavSys = nil
	} else if p.format != nil {
		p.format.Close()
	}
	p.format = nil

	// Close audio decoder
	if p.audioDecoder != nil {
		libav.LibAVAudioClose(p.audioDecoder)
		p.audioDecoder = nil
	}

	// Free codec contexts
	if p.videoCodec != nil && p.videoCodec.Ctx != nil {
		baselibav.AvcodecFreeContext(p.videoCodec.Ctx)
		p.videoCodec.Ctx = nil
		p.videoCodec = nil
	}
	if p.audioCodec != nil && p.audioCodec.Ctx != nil {
		baselibav.AvcodecFreeContext(p.audioCodec.Ctx)
		p.audioCodec.Ctx = nil
		p.audioCodec = nil
	}

	p.videoDecoder = nil

	// SUB-1 Selection Wiring: unsubscribe from subtitle/current prop
	// C: mp_track_mgr_destroy → prop_unsubscribe(mtm->mtm_current_sub)
	// This prevents callback firing on a destroyed pipeline.
	if p.subCurrentSub != nil {
		p.subCurrentSub.Unsubscribe()
		p.subCurrentSub = nil
	}

	// Clear frame pacer queue
	if p.framePacer != nil {
		p.framePacer.Clear()
	}

	if p.propPlayStatus != nil {
		p.propMgr.SetStringEx(p.propPlayStatus, nil, "stop", 0)
	}

	// Clear loading state on stop
	if p.propLoading != nil {
		p.propMgr.SetIntEx(p.propLoading, nil, 0)
	}

	// C: mp_destroy — release the media pipe created by MpCreate
	if p.mediaPipe != nil {
		mediacore.MpDestroy(p.mediaPipe)
		p.mediaPipe = nil
	}
}

// partialCleanup closes decoders and ALSA but keeps the format context open.
// This allows seek-after-EOF: the user can seek to a position and resume
// playback without reopening the file.
// Mirrors C's behavior where the format context is not closed at EOF.
func (p *PlaybackPipeline) partialCleanup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cleanedUp {
		return
	}

	// Close audio decoder
	if p.audioDecoder != nil {
		libav.LibAVAudioClose(p.audioDecoder)
		p.audioDecoder = nil
	}

	// Free codec contexts (but NOT the format context)
	if p.videoCodec != nil && p.videoCodec.Ctx != nil {
		baselibav.AvcodecFreeContext(p.videoCodec.Ctx)
		p.videoCodec.Ctx = nil
		p.videoCodec = nil
	}
	if p.audioCodec != nil && p.audioCodec.Ctx != nil {
		baselibav.AvcodecFreeContext(p.audioCodec.Ctx)
		p.audioCodec.Ctx = nil
		p.audioCodec = nil
	}

	p.videoDecoder = nil

	// Clear frame pacer queue
	if p.framePacer != nil {
		p.framePacer.Clear()
	}

	if p.propPlayStatus != nil {
		p.propMgr.SetStringEx(p.propPlayStatus, nil, "stop", 0)
	}

	// NOTE: p.format is intentionally NOT closed here.
	// This allows SeekToPosition() to work after EOF.
	// The format will be closed by Stop() or Open().
}
