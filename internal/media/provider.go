package media

import propcore "github.com/czz/movian-go/internal/prop"

// MediaPipeline defines the interface for media subsystem management.
// Consumers should depend on this interface, not the concrete MediaSystem.
type MediaPipeline interface {
	// Start initializes the media subsystem
	Start() error

	// Fini cleans up the media subsystem
	Fini()

	// GetPropRoot returns the media property root
	GetPropRoot() *propcore.Prop

	// GetPropSources returns the media sources property
	GetPropSources() *propcore.Prop

	// GetPropCurrent returns the media current property
	GetPropCurrent() *propcore.Prop
}

// Compile-time assertion that MediaSystem implements MediaPipeline
var _ MediaPipeline = (*MediaSystem)(nil)
