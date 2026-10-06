// RTMP be_play_video via FFmpeg's native rtmp:// protocol + live_flv
// demuxer — the implementation on every platform. It replaced the
// vendored ext/rtmpdump/librtmp transport (rtmplib subpackage, now
// deleted) after live testing showed the rtmpdump-era handshake fails
// against modern RTMP servers (mediamtx) while the FFmpeg path plays
// correctly.
//
// The function mirrors rtmp_playvideo (rtmp.c:662-766): same
// mp_set_url / "format"=RTMP metadata, NoFSScan flag and
// become-primary semantics; restart position and seek are handled by
// the shared video_player_loop tail (bePlayvideoFctx) instead of
// RTMP_SendSeek, and stream/codec setup comes from avformat stream
// info instead of the AMF onMetaData script.

package rtmp

import (
	"errors"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
)

// rtmpPlayvideoAV — C: rtmp_playvideo wrapper (rtmp.c:662-766), with
// the rtmplib connect/loop replaced by avformat_open_input(rtmp://…)
// + the shared fa_video playback tail.
func (b *Backend) rtmpPlayvideoAV(bs *backendcore.BackendSystem,
	url string, mp *mediacore.MediaPipe,
	va *backendcore.VideoArgs) (any, error) {

	// C: mp_set_url + "format"="RTMP" + loading prop (rtmp.c:673-683)
	va2 := *va
	mediacore.MpSetURL(mp, va.CanonicalURL, va.ParentURL, va.ParentTitle)
	bs.Usage().Event("Play video", 1, "format", "RTMP")
	if pm := mp.PropMetadata.Manager(); pm != nil {
		pm.SetVEx(nil, mp.PropMetadata, "format", "RTMP")
	}
	if pm := mp.PropRoot.Manager(); pm != nil {
		pm.SetVEx(nil, mp.PropRoot, "loading", 1)
	}

	// C: va.flags |= BACKEND_VIDEO_NO_FS_SCAN (rtmp.c:685)
	va2.Flags |= backendcore.BackendVideoNoFSScan

	// Direct-URL avformat open — rtmp:// is handled by FFmpeg's own
	// URLProtocol (CONFIG_RTMP_PROTOCOL in the vendored build) and the
	// stream demuxed by live_flv. No AVIOContext exists for RTMP.
	libavSys := libav.GetGlobalLibAVSystem()
	fctxLibav, err := libav.FALibavOpenFormat(nil, url, "",
		libav.FaLibavOpenStrategyVideoNonSeekable)
	if fctxLibav == nil {
		if err == nil {
			err = errors.New("Unable to open RTMP stream")
		}
		return nil, err
	}
	defer libav.FALibavCloseFormat(libavSys, fctxLibav, false)

	// C: mp_become_primary(mp) (rtmp.c:730)
	mediacore.MpBecomePrimary(mp, mp.PropRoot.Manager())

	// Shared fa_video tail: stream/codec setup, subtitle scan, indexes
	// and video_player_loop — fh=nil (no fileaccess handle for rtmp).
	return bs.BePlayvideoFctx(url, mp, nil, &va2, fctxLibav)
}
