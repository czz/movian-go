package video

import (
	"sync"
	"unsafe"

	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// Port of src/video/video_settings.h — the video_settings global.
// video_settings_init lives in pkg/media/settings (media_settings.go)
// because it needs the setting_create machinery; it writes through the
// field addresses exactly like C's SETTING_WRITE_INT/SETTING_WRITE_BOOL.

// C: video_settings_t.dpad_up_down_mode enum (video_settings.h:40-43)
const (
	VideoDpadMasterVolume  = 0 // VIDEO_DPAD_MASTER_VOLUME
	VideoDpadPerFileVolume = 1 // VIDEO_DPAD_PER_FILE_VOLUME
)

// VideoSettings — C: struct video_settings (video_settings.h)
type VideoSettings struct {
	Vaapi              int // extension — no C counterpart
	Nvdec              int // extension — no C counterpart
	D3d11va            int // windows extension — no C counterpart
	Dxva2              int // windows extension — no C counterpart
	VideoAccel         int
	Vzoom              int
	PanHorizontal      int
	PanVertical        int
	ScaleHorizontal    int
	ScaleVertical      int
	StretchHorizontal  int
	StretchFullscreen  int
	Vinterpolate       int
	ResumeMode         int
	DpadUpDownMode     int
	PlayedThreshold    int
	ContinuousPlayback int
	SeekBackStep       int // C: seek_back_step
	SeekFwdStep        int // C: seek_fwd_step
	VideoBufferSize    int // C: video_buffer_size

	// PropManager — the prop manager the settings were created under.
	// C: prop_get_global() inside video_settings_init.
	PropManager *propcore.PropManager

	// VaapiEGL — session VAAPI/EGL capability state published by the
	// GL backend and read by the libav deliver path (extension, no C
	// counterpart — was a package-global vaapiEGL).
	VaapiEGL VaapiEGLCaps

	// Hw device caches (extensions — were process-global vaapiDev /
	// cudaDev + sync.Once in pkg/media/libav). Lazily created on first
	// codec bind; nil when the backend is unavailable.
	VaapiDev     unsafe.Pointer // AVBufferRef*
	VaapiDevOnce sync.Once
	CudaDev      unsafe.Pointer // AVBufferRef*
	CudaDevOnce  sync.Once
	// HwProbeOnce — one-shot hw-accel probe latch (was package-global
	// hwProbeOnce in pkg/media/libav).
	HwProbeOnce sync.Once

	// C: video_settings.*_setting (setting_t *) — the global default
	// settings used as SETTING_INHERIT parents by mp_settings_init
	// (media_settings.c). Created by pkg/media/settings.
	VzoomSetting             *settingscore.Setting
	PanHorizontalSetting     *settingscore.Setting
	PanVerticalSetting       *settingscore.Setting
	ScaleHorizontalSetting   *settingscore.Setting
	ScaleVerticalSetting     *settingscore.Setting
	StretchHorizontalSetting *settingscore.Setting
	StretchFullscreenSetting *settingscore.Setting
	VinterpolateSetting      *settingscore.Setting
}

// C: struct video_settings video_settings (video_settings.c:28) — the
// ambient global. In Go the instance is owned by the composition root
// (cmd/movian-go) and published to consumers through mediacore's
// SetVideoSettings seam. The seam is never nil (see lifecycle.go), so
// field reads before wiring return zeros, like C's zero-init global.

// NewVideoSettings creates the settings object — the seam replacing
// C's prop_get_global() inside video_settings_init. The caller owns
// the instance and publishes it via mediacore.SetVideoSettings.
func NewVideoSettings(pm *propcore.PropManager) *VideoSettings {
	return &VideoSettings{PropManager: pm}
}
