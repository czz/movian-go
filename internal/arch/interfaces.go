package arch

import (
	"os"
	"unsafe"
)

// FileSystemProvider defines file system operations abstracted from the arch package.
type FileSystemProvider interface {
	FileExists(path string) bool
	IsDir(path string) bool
	GetFileSize(path string) (int64, error)
	GetFileModTime(path string) (int64, error)
	Mkdir(path string) error
	MkdirAll(path string) error
	Remove(path string) error
	RemoveAll(path string) error
	Rename(oldpath, newpath string) error
}

// EnvProvider defines environment variable operations.
type EnvProvider interface {
	GetEnv(key string) string
	SetEnv(key, value string) error
	UnsetEnv(key string) error
}

// OSInfoProvider defines OS detection and information operations.
type OSInfoProvider interface {
	GetOS() string
	GetArch() string
	IsLinux() bool
	IsDarwin() bool
	IsWindows() bool
	IsAndroid() bool
	GetSystemType() string
	GetDistribution() string
	GetOSInfo() string
}

// PathProvider defines path resolution operations.
type PathProvider interface {
	GetCachePath() string
	GetPersistentPath() string
	GetHomeDir() (string, error)
	GetTempDir() string
	SyncPath(path string) error
}

// ThreadProvider defines threading operations.
type ThreadProvider interface {
	ThreadCreateDetached(name string, fn func(aux any) any, aux any, prio int)
	ThreadCreateJoinable(name string, fn func(aux any) any, aux any, prio int) *Thread
	SetSignalHandler(fn func(sig os.Signal))
}

// MemoryProvider defines memory allocation operations.
type MemoryProvider interface {
	MallocSize(ptr unsafe.Pointer) uintptr
	Halloc(size int) ([]byte, error)
	Hfree(mem []byte)
	MyMalloc(size int) []byte
	MyRealloc(ptr []byte, size int) []byte
	MyCalloc(count, size int) []byte
	MyMemalign(align, size int) []byte
}

// SyslogProvider defines syslog operations.
type SyslogProvider interface {
	OpenSyslog(ident string)
	Closelog()
	Syslog(priority int, format string, args ...any)
}
