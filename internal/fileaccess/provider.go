package fileaccess

// FileAccessProvider defines the interface for file access management.
// Consumers should depend on this interface, not the concrete FileAccessManager.
type FileAccessProvider interface {
	// Start initializes the file access manager and registers all protocols
	Start() error

	// FindProtocol finds a protocol that can handle the given URL
	FindProtocol(url string) Protocol

	// RegisterProtocol registers a protocol handler for file access
	RegisterProtocol(proto Protocol)

	// GetBundleManager returns the bundle manager for in-memory file operations
	GetBundleManager() *BundleManager
}

// Compile-time assertion that FileAccessManager implements FileAccessProvider
var _ FileAccessProvider = (*FileAccessManager)(nil)
