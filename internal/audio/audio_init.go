package audio

import (
	"github.com/czz/movian-go/internal/audio/core"
	"github.com/czz/movian-go/internal/htsmsg"
	propcore "github.com/czz/movian-go/internal/prop"
)

// NewAudioManager creates a new audio manager instance
func NewAudioManager(pm *propcore.PropManager,
	store *htsmsg.Store) *core.AudioManager {
	return core.NewAudioManager(pm, store)
}
