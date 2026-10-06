package media

import (
	"sync"

	mediacore "github.com/czz/movian-go/internal/media/core"
	propcore "github.com/czz/movian-go/internal/prop"
)

// MediaSystem manages media subsystem without global state
type MediaSystem struct {
	mutex       sync.RWMutex
	core        *mediacore.MediaSystem // C: media.c globals
	propRoot    *propcore.Prop
	propSources *propcore.Prop
	propCurrent *propcore.Prop
	propMgr     *propcore.PropManager
}

// NewMediaSystem creates a new media system
func NewMediaSystem(pm *propcore.PropManager) *MediaSystem {
	return &MediaSystem{
		propMgr: pm,
	}
}

// Start initializes the media subsystem
func (ms *MediaSystem) Start() error {
	ms.mutex.Lock()
	defer ms.mutex.Unlock()

	// C: media_init (media.c:77-94) — media_codec_init, media/sources/
	// current prop nodes, and the media_global_eventsink subscription.
	ms.core = mediacore.MediaStart(ms.propMgr)
	ms.propRoot = ms.core.PropRoot
	ms.propSources = ms.core.PropSources
	ms.propCurrent = ms.core.PropCurrent

	return nil
}

// Fini cleans up the media subsystem
func (ms *MediaSystem) Fini() {
	ms.mutex.Lock()
	defer ms.mutex.Unlock()

	if ms.propRoot != nil {
		ms.propMgr.Destroy(ms.propRoot)
		ms.propRoot = nil
		ms.propSources = nil
		ms.propCurrent = nil
	}
}

// GetPropRoot returns the media property root
func (ms *MediaSystem) GetPropRoot() *propcore.Prop {
	ms.mutex.RLock()
	defer ms.mutex.RUnlock()
	return ms.propRoot
}

// GetPropSources returns the media sources property
func (ms *MediaSystem) GetPropSources() *propcore.Prop {
	ms.mutex.RLock()
	defer ms.mutex.RUnlock()
	return ms.propSources
}

// GetPropCurrent returns the media current property
func (ms *MediaSystem) GetPropCurrent() *propcore.Prop {
	ms.mutex.RLock()
	defer ms.mutex.RUnlock()
	return ms.propCurrent
}

// Core returns the underlying mediacore system (C: media.c globals).
func (ms *MediaSystem) Core() *mediacore.MediaSystem { return ms.core }
