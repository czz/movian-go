package arch

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/trace"
)

// Stop request return values
const (
	ArchStopIsProgressing = iota
	ARCH_STOP_IS_NOT_HANDLED
	ARCH_STOP_CALLER_MUST_HANDLE
)

// System represents the system architecture and state
type System struct {
	mu             sync.RWMutex
	devURandom     *os.File
	stopReq        bool
	canSetPriority bool
	deviceID       string
	initialized    bool
}

// NewSystem creates a new System instance
func NewSystem() *System {
	return &System{}
}

// Start initializes the system
func (s *System) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.initialized {
		return nil
	}

	// Try to open /dev/urandom on Unix-like systems
	if runtime.GOOS != "windows" {
		var err error
		s.devURandom, err = os.Open("/dev/urandom")
		if err != nil {
			s.devURandom = nil
		}

		// Ignore SIGPIPE on Unix-like systems
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGPIPE)
		go func() {
			for range sigChan {
			}
		}()
	}

	// Initialize device ID
	s.deviceID = s.generateDeviceID()

	// Check capabilities
	s.canSetPriority = s.checkCapabilities()

	s.initialized = true
	return nil
}

// Fini shuts down the system
func (s *System) Fini() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.devURandom != nil {
		s.devURandom.Close()
		s.devURandom = nil
	}
	s.initialized = false
}

// GetTS returns the current timestamp in microseconds
func GetTS() int64 {
	return time.Now().UnixNano() / 1000
}

// SyncPath syncs a path to disk
func SyncPath(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// StopReq checks if a stop is requested
func (s *System) StopReq() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.stopReq {
		return ArchStopIsProgressing
	}
	return ARCH_STOP_IS_NOT_HANDLED
}

// SetStopReq sets the stop request flag
func (s *System) SetStopReq(req bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopReq = req
}

// Localtime converts time to local time
func Localtime(t time.Time) time.Time {
	return t.Local()
}

// GetRandomBytes fills a buffer with random bytes
func (s *System) GetRandomBytes(ptr []byte) error {
	s.mu.RLock()
	devURandom := s.devURandom
	s.mu.RUnlock()

	if devURandom != nil {
		_, err := devURandom.Read(ptr)
		return err
	}
	_, err := rand.Read(ptr)
	return err
}

// MallocSize returns the size of an allocation
// Go's runtime does not expose allocation sizes, so this returns 0
// This is a fundamental limitation of Go's memory model
func MallocSize(ptr unsafe.Pointer) uintptr {
	return 0
}

// Halloc allocates huge page memory
func Halloc(size int) ([]byte, error) {
	return make([]byte, size), nil
}

// Hfree frees huge page memory
func Hfree(mem []byte) {
	// Go's GC handles this
}

// MyMalloc allocates memory
func MyMalloc(size int) []byte {
	return make([]byte, size)
}

// MyRealloc reallocates memory
func MyRealloc(ptr []byte, size int) []byte {
	newMem := make([]byte, size)
	copy(newMem, ptr)
	return newMem
}

// MyCalloc allocates and zeros memory
func MyCalloc(count, size int) []byte {
	return make([]byte, count*size)
}

// MyMemalign allocates aligned memory — C: mymemalign (posix.c:312)
// wrapping posix_memalign. Go has no aligned allocator, so we
// over-allocate and return an aligned subslice; the slice header keeps
// the whole backing array alive.
func MyMemalign(align, size int) []byte {
	if align <= 0 || size <= 0 {
		return make([]byte, size)
	}
	buf := make([]byte, size+align-1)
	off := int(uintptr(unsafe.Pointer(&buf[0])) % uintptr(align))
	if off != 0 {
		off = align - off
	}
	return buf[off : off+size]
}

// GetCWD returns the current working directory
func GetCWD() (string, error) {
	return os.Getwd()
}

// SetCWD sets the current working directory
func SetCWD(path string) error {
	return os.Chdir(path)
}

// FileExists checks if a file exists
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IsDir checks if a path is a directory
func IsDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// GetFileSize returns the size of a file
func GetFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// GetFileModTime returns the modification time of a file
func GetFileModTime(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.ModTime().Unix(), nil
}

// Mkdir creates a directory
func Mkdir(path string) error {
	return os.Mkdir(path, 0755)
}

// MkdirAll creates a directory and all parent directories
func MkdirAll(path string) error {
	return os.MkdirAll(path, 0755)
}

// Remove removes a file or directory
func Remove(path string) error {
	return os.Remove(path)
}

// RemoveAll removes a directory and all its contents
func RemoveAll(path string) error {
	return os.RemoveAll(path)
}

// Rename renames a file or directory
func Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

// GetEnv gets an environment variable
func GetEnv(key string) string {
	return os.Getenv(key)
}

// SetEnv sets an environment variable
func SetEnv(key, value string) error {
	return os.Setenv(key, value)
}

// UnsetEnv unsets an environment variable
func UnsetEnv(key string) error {
	return os.Unsetenv(key)
}

// GetHostname returns the system hostname
func GetHostname() (string, error) {
	return os.Hostname()
}

// GetPageSize returns the system page size
func GetPageSize() int {
	return os.Getpagesize()
}

// GetPID returns the current process ID
func GetPID() int {
	return os.Getpid()
}

// GetPPID returns the parent process ID
func GetPPID() int {
	return os.Getppid()
}

// Sleep sleeps for the specified duration in milliseconds
func Sleep(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// USleep sleeps for the specified duration in microseconds
func USleep(us int) {
	time.Sleep(time.Duration(us) * time.Microsecond)
}

// GetNumCPUs returns the number of CPUs
func GetNumCPUs() int {
	return runtime.NumCPU()
}

// GetOS returns the operating system name
func GetOS() string {
	return runtime.GOOS
}

// GetArch returns the system architecture
func GetArch() string {
	return runtime.GOARCH
}

// IsLinux checks if the OS is Linux
func IsLinux() bool {
	return runtime.GOOS == "linux"
}

// IsDarwin checks if the OS is macOS
func IsDarwin() bool {
	return runtime.GOOS == "darwin"
}

// IsWindows checks if the OS is Windows
func IsWindows() bool {
	return runtime.GOOS == "windows"
}

// IsAndroid checks if the OS is Android
func IsAndroid() bool {
	return runtime.GOOS == "android"
}

// GetUserName returns the current username
func GetUserName() (string, error) {
	return os.Getenv("USER"), nil
}

// GetHomeDir returns the user's home directory
func GetHomeDir() (string, error) {
	return os.UserHomeDir()
}

// GetTempDir returns the system temporary directory
func GetTempDir() string {
	return os.TempDir()
}

// GetSystemConcurrency returns the number of available CPUs
func GetSystemConcurrency() int {
	return runtime.NumCPU()
}

// generateDeviceID generates a unique device ID based on MAC address
func (s *System) generateDeviceID() string {
	// Try to read MAC address from common locations
	paths := []string{
		"/sys/class/net/eth0/address",
		"/sys/class/net/en0/address",
		"/sys/class/net/wlan0/address",
	}

	for _, path := range paths {
		if data, err := os.ReadFile(path); err == nil {
			mac := strings.TrimSpace(string(data))
			if mac != "" {
				// Generate MD5 hash of MAC address
				h := md5.New()
				h.Write([]byte(mac))
				digest := h.Sum(nil)
				return hex.EncodeToString(digest)
			}
		}
	}

	// Fallback to random ID
	randomBytes := make([]byte, 16)
	rand.Read(randomBytes)
	h := md5.New()
	h.Write(randomBytes)
	digest := h.Sum(nil)
	return hex.EncodeToString(digest)
}

// GetDeviceID returns the unique device identifier (MD5 of MAC or random).
func (s *System) GetDeviceID() string {
	s.mu.RLock()
	id := s.deviceID
	s.mu.RUnlock()
	if id != "" {
		return id
	}
	id = s.generateDeviceID()
	s.mu.Lock()
	s.deviceID = id
	s.mu.Unlock()
	return id
}

// checkCapabilities checks if the system can set thread priorities
func (s *System) checkCapabilities() bool {
	// On Linux, check for CapSysNice capability
	if IsLinux() {
		// In Go, we can't directly check capabilities
		// Assume we can set priorities if running as root
		if GetPID() == 0 || syscall.Getuid() == 0 {
			return true
		}
	}
	return false
}

// CanSetThreadPriorities returns whether thread priorities can be set
func CanSetThreadPriorities(s *System) bool {
	if s == nil {
		return false
	}
	return s.canSetPriority
}

// GetDistribution returns the Linux distribution name
func GetDistribution() string {
	if !IsLinux() {
		return ""
	}

	// Try to read from /etc/os-release
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		lines := strings.SplitSeq(string(data), "\n")
		for line := range lines {
			if after, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				name := strings.Trim(after, "\"")
				return name
			}
		}
	}

	return "Linux"
}

// GetOSInfo returns OS information
func GetOSInfo() string {
	// C: posix.c:117-120 — #ifdef STOS dist = stos_get_dist();
	//     if(dist == NULL) dist = linux_get_dist();
	dist := mgosGetDist()
	if dist == "" {
		dist = GetDistribution()
	}
	if dist != "" {
		return dist
	}
	return fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
}

// GetCachePath returns the cache path
func GetCachePath() string {
	home, err := GetHomeDir()
	if err != nil {
		return "/tmp"
	}
	return fmt.Sprintf("%s/.cache/movian-go", home)
}

// GetPersistentPath returns the persistent data path
func GetPersistentPath() string {
	home, err := GetHomeDir()
	if err != nil {
		return "/tmp"
	}
	return fmt.Sprintf("%s/.movian-go", home)
}

// OpenSyslog opens syslog connection
func OpenSyslog(ident string) {
	// Go's log/syslog package can be used if needed
	// fallback
}

// Closelog closes syslog connection
func Closelog() {
	// fallback
}

// Syslog logs to syslog
func Syslog(priority int, format string, args ...any) {
	// fallback - could use log/syslog package
}

// Exit terminates the program
func Exit() {
	os.Exit(1)
}

// Stackdump dumps the current stack trace
func Stackdump(logprefix string) {
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, true)
	archTS.ts.Debug("arch", "%s: %s\n", logprefix, string(buf[:n]))
}

// ---------------------------------------------------------------------------
// Mutex / Cond — thin wrappers that mirror the C arch_mutex_t / arch_cond_t
// ---------------------------------------------------------------------------

// Mutex is a read-write mutex compatible with the C arch_mutex_t interface.
type Mutex struct {
	sync.RWMutex
}

// SetupMutex initialises m (no-op in Go; zero value is ready to use).
func SetupMutex(m *Mutex) {}

// Cond is a condition variable paired with a Mutex.
type Cond struct {
	c sync.Cond
}

// SetupCond initialises c and associates it with m.
func SetupCond(c *Cond, m *Mutex) {
	c.c = sync.Cond{L: &m.RWMutex}
}

// CondWaitTimeout waits on c for at most timeoutMs milliseconds.
// Returns true if the wait timed out, false if signalled in time.
func CondWaitTimeout(c *Cond, m *Mutex, timeoutMs int) bool {
	timedOut := false
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
		select {
		case <-timer.C:
			m.Lock()
			timedOut = true
			c.c.Broadcast()
			m.Unlock()
		case <-done:
			timer.Stop()
		}
	}()
	c.c.Wait()
	close(done)
	return timedOut
}

// C: THREAD_PRIO_* (src/arch/posix/posix_threads.h:78-88). Go ignores
// goroutine priorities; values kept for call-site parity.
const (
	ThreadPrioAudio        = -10
	ThreadPrioVideo        = -5
	ThreadPrioDemuxer      = 3
	ThreadPrioUIWorkerHigh = 5
	ThreadPrioUIWorkerMed  = 8
	ThreadPrioFilesystem   = 10
	ThreadPrioModel        = 12
	ThreadPrioMetadata     = 13
	ThreadPrioUIWorkerLow  = 14
	ThreadPrioMetadataBg   = 15
	ThreadPrioBgtask       = 19
)

// threadAttachHook/threadDetachHook — C: the
// AttachCurrentThread/DetachCurrentThread pair in thread_trampoline
// (android_threads.c). Nil on desktop; set by android_jni.go's init.
var threadAttachHook, threadDetachHook func()

// archTS — C: trace() global reached by thread_trampoline /
// hts_thread_create_* (threads.c). Injected by the composition root.
var archTS struct {
	ts    *trace.TraceSystem
	gconf *gconf.T // C: gconf_t — injected
}

// SetTraceSystem injects the trace system (C: trace() global).
func SetTraceSystem(ts *trace.TraceSystem) { archTS.ts = ts }

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func SetGconf(g *gconf.T) { archTS.gconf = g }

// ThreadCreateDetached spawns fn in a detached goroutine (matches C arch_thread_create).
func ThreadCreateDetached(name string, fn func(aux any) any, aux any, prio int) {
	go func() {
		if threadAttachHook != nil {
			threadAttachHook()
			defer threadDetachHook()
		}
		fn(aux)
		// C: thread_trampoline — if(gconf.enable_thread_debug)
		//    TRACE(TRACE_DEBUG, "thread", "Thread %s exited", title)
		if g := archTS.gconf; g != nil && g.EnableThreadDebug.Load() {
			archTS.ts.Trace(trace.TRACE_DEBUG, "thread",
				"Thread %s exited", name)
		}
	}()
	// C: tracelog(TRACE_NO_PROP, TRACE_DEBUG, "thread",
	//   "Created detached thread: %s", title)
	if g := archTS.gconf; g != nil && g.EnableThreadDebug.Load() {
		archTS.ts.Tracev(trace.TraceNoProp, trace.TRACE_DEBUG, "thread",
			"Created detached thread: %s", name)
	}
}

// Thread represents a joinable goroutine.
type Thread struct {
	result chan any
}

// Join waits for the thread to finish and returns its result.
func (t *Thread) Join() any {
	return <-t.result
}

// ThreadCreateJoinable spawns fn in a goroutine and returns a handle to join it.
func ThreadCreateJoinable(name string, fn func(aux any) any, aux any, prio int) *Thread {
	th := &Thread{result: make(chan any, 1)}
	go func() {
		if threadAttachHook != nil {
			threadAttachHook()
			defer threadDetachHook()
		}
		r := fn(aux)
		// C: thread_trampoline — if(gconf.enable_thread_debug)
		//    TRACE(TRACE_DEBUG, "thread", "Thread %s exited", title)
		if g := archTS.gconf; g != nil && g.EnableThreadDebug.Load() {
			archTS.ts.Trace(trace.TRACE_DEBUG, "thread",
				"Thread %s exited", name)
		}
		th.result <- r
	}()
	// C: tracelog(TRACE_NO_PROP, TRACE_DEBUG, "thread",
	//   "Created thread: %s", title)
	if g := archTS.gconf; g != nil && g.EnableThreadDebug.Load() {
		archTS.ts.Tracev(trace.TraceNoProp, trace.TRACE_DEBUG, "thread",
			"Created thread: %s", name)
	}
	return th
}

// SetSignalHandler registers a OS signal handler (SIGINT / SIGTERM by default).
func SetSignalHandler(fn func(sig os.Signal)) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range ch {
			fn(sig)
		}
	}()
}

// gcfg.EnableThreadDebug — C: gconf.enable_thread_debug (main.h:254),
// written by the "threadsdebug" dev bool in init_dev_settings. Gates the
// thread create/exit traces (C: posix_threads.c trampoline).
