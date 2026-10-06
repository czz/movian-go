//go:build linux

package arch

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/version"
	"golang.org/x/sys/unix"
)

// Linux-specific architecture implementation

// LinuxSystem represents Linux-specific system functionality
type LinuxSystem struct {
	mu sync.Mutex

	// Thread priority capability
	setThreadPriorities bool

	// Property manager

	// Service system
	serviceSystem *service.ServiceSystem

	// Process monitor (C: linux_process_monitor.c's statics)
	procMon *ProcessMonitor

	// Device ID
	deviceID string
}

// NewLinuxSystem creates a new LinuxSystem instance
func NewLinuxSystem() *LinuxSystem {
	return &LinuxSystem{
		setThreadPriorities: false,
	}
}

// StartLinux initializes Linux-specific components
func StartLinux(ls *LinuxSystem) error {
	return ls.Start()
}

// Start initializes the Linux system
func (ls *LinuxSystem) Start() error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	// Get device ID from MAC address
	ls.getDeviceID()

	// C: linux_trap_init() — installs the SIGSEGV-family handlers.
	// appversion + argv[0] feed the crash banner and .syms lookup.
	LinuxTrapStart(version.AppVersion(), os.Args[0])

	// C: gconf.concurrency = get_system_concurrency() — the caller
	// assigns arch.GetSystemConcurrency() (sched_getaffinity count).
	return nil
}

// GetLinuxVersion returns the Linux kernel version
func GetLinuxVersion() string {
	var uname syscall.Utsname
	if err := syscall.Uname(&uname); err != nil {
		return "unknown"
	}

	// Convert [65]byte to string
	release := make([]byte, 0, 65)
	for _, b := range uname.Release {
		if b == 0 {
			break
		}
		release = append(release, byte(b))
	}
	return string(release)
}

// GetLinuxDistro returns the Linux distribution
func GetLinuxDistro() string {
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

	// Fallback to /etc/lsb-release
	if data, err := os.ReadFile("/etc/lsb-release"); err == nil {
		lines := strings.SplitSeq(string(data), "\n")
		for line := range lines {
			if after, ok := strings.CutPrefix(line, "DISTRIB_DESCRIPTION="); ok {
				name := strings.Trim(after, "\"")
				return name
			}
		}
	}

	return "Linux"
}

// LinuxMainStart initializes Linux main
func LinuxMainStart(ls *LinuxSystem, ss *service.ServiceSystem) error {
	return ls.MainStart(ss)
}

// MainStart — C: the add_xdg_paths() call in linux_main.c's main()
// (runs after service system init).
func (ls *LinuxSystem) MainStart(ss *service.ServiceSystem) error {
	ls.serviceSystem = ss
	ls.addXDGPaths()
	return nil
}

// LinuxProcessMonitorStart initializes Linux process monitor
func LinuxProcessMonitorStart(ls *LinuxSystem, pm *propcore.PropManager) error {
	return ls.ProcessMonitorStart(pm)
}

// ProcessMonitorStart — C: linux_process_monitor_init
// (linux_process_monitor.c:175-180) + INITME registration. Creates
// "system" under the global prop root and starts the monitor.
func (ls *LinuxSystem) ProcessMonitorStart(pm *propcore.PropManager) error {
	// C: p_sys = prop_create(prop_get_global(), "system")
	systemProp := pm.CreateEx(pm.GetGlobal(), "system", nil, false, false)
	ls.procMon = NewProcessMonitor(systemProp, pm)
	ls.procMon.Start()
	return nil
}

// LinuxCheckCapabilities checks Linux capabilities
func LinuxCheckCapabilities(ls *LinuxSystem) {
	ls.CheckCapabilities()
}

// CheckCapabilities checks Linux capabilities
func (ls *LinuxSystem) CheckCapabilities() {
	// Linux capability structures (from linux/capability.h)
	type capUserHeader struct {
		version uint32
		pid     int32
	}

	type capUserData struct {
		effective   uint32
		permitted   uint32
		inheritable uint32
	}

	const (
		CapSysNice                  = 23
		_LINUX_CAPABILITY_VERSION_3 = 0x20080522
		SysCapget                   = 184 // __NR_capget on x86_64
	)

	header := capUserHeader{
		version: _LINUX_CAPABILITY_VERSION_3,
		pid:     int32(syscall.Getpid()),
	}

	data := [3]capUserData{}

	// Call capget syscall
	_, _, errno := syscall.Syscall6(SysCapget,
		uintptr(unsafe.Pointer(&header)),
		uintptr(unsafe.Pointer(&data)),
		0, 0, 0, 0)

	if errno != 0 {
		// Failed to get capabilities, fallback to checking if running as root
		if syscall.Getuid() == 0 {
			ls.setThreadPriorities = true
		}
		return
	}

	// Check if CapSysNice is in effective capabilities
	if data[0].effective&(1<<CapSysNice) != 0 {
		ls.setThreadPriorities = true
	}
}

// SetThreadPriorities — C: posix_set_thread_priorities (posix_threads.c:36)
// — whether CAP_SYS_NICE/root allows realtime thread priorities. Read by
// the rpi/sunxi main() warning (rpi_main.c:931-942).
func (ls *LinuxSystem) SetThreadPriorities() bool {
	return ls.setThreadPriorities
}

// SyncPathLinux — C: arch_sync_path (linux_misc.c:114-122). Opens the
// path read-only and issues syncfs(2) on the fd (not a global sync).
func SyncPathLinux(path string) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		return err // C: silent return
	}
	defer syscall.Close(fd)

	unix.Syncfs(fd)
	return nil
}

// getDeviceID — C: get_device_id (linux_misc.c:129-153). Reads the
// eth0 MAC and stores its MD5 hex in gconf.device_id. C hashes the
// raw bytes read (including the trailing newline) — preserved here.
func (ls *LinuxSystem) getDeviceID() {
	data, err := os.ReadFile("/sys/class/net/eth0/address")
	if err != nil || len(data) < 1 {
		return
	}
	digest := md5.Sum(data)
	ls.deviceID = hex.EncodeToString(digest[:])
}

// DeviceID returns the MAC-derived device ID (C: gconf.device_id as
// set by get_device_id; empty when eth0 is absent).
func (ls *LinuxSystem) DeviceID() string {
	return ls.deviceID
}

// addXDGPath adds an XDG user directory path
func (ls *LinuxSystem) addXDGPath(class, serviceType string) {
	cmd := exec.Command("xdg-user-dir", class)
	output, err := cmd.Output()
	if err != nil {
		return
	}

	path := strings.TrimSpace(string(output))
	if path == "" {
		return
	}

	title := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		title = path[idx+1:]
	}

	id := fmt.Sprintf("xdg-user-dir-%s", class)

	// C: service_create_managed(id, title, path, type, NULL, 0, 1,
	// SVC_ORIGIN_SYSTEM) — the raw path is passed as the URL.
	if ls.serviceSystem != nil {
		ls.serviceSystem.ServiceCreateManaged(id, title, path, serviceType, "", false, true, service.SvcOriginSystem)
	}
}

// addXDGPaths — C: add_xdg_paths (linux_main.c:227-232). Exactly
// MUSIC, PICTURES and VIDEOS — no other XDG classes.
func (ls *LinuxSystem) addXDGPaths() {
	ls.addXDGPath("MUSIC", "music")
	ls.addXDGPath("PICTURES", "photos")
	ls.addXDGPath("VIDEOS", "video")
}

// GetSystemTypeLinux — C: arch_get_system_type (linux_misc.c:54-64).
// Returns one of the canonical fixed strings, not a generic GOARCH.
func GetSystemTypeLinux() string {
	switch runtime.GOARCH {
	case "386":
		return "Linux/i386"
	case "amd64":
		return "Linux/x86_64"
	case "arm":
		return "Linux/arm"
	default:
		return "Linux/other"
	}
}
