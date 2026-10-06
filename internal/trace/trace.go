package trace

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	propcore "github.com/czz/movian-go/internal/prop"
)

// Trace levels
const (
	TraceEmerg = iota
	TRACE_ERROR
	TRACE_INFO
	TRACE_DEBUG
)

// Trace flags
const (
	TraceNoProp = 1 << iota
)

// TraceSystem manages trace logging state
type TraceSystem struct {
	traceMutex   sync.Mutex
	traceStarted bool
	traceLevel   int
	logFile      *os.File
	logStartTS   int64
	uiLogLines   int
	logEntries   int

	// UI log buffer — C: log_root prop (trace.c:35). Each traced line
	// becomes a child prop with prefix/message/severity; children
	// beyond UI_LOG_LINES are destroyed oldest-first.
	logRoot *propcore.Prop

	// Network logging
	netLogEnabled bool
	netLogPort    int
	netLogAddr    string
	netLogConn    *net.UDPConn
	netLogServer  *net.UDPAddr

	// Debug flags
	enableMetadataDebug bool

	// Platform-specific logging callback (trace_arch equivalent)
	platformLogger func(level int, prefix string, message string)

	// Property statistics
	propStatsStarted bool
	pm               *propcore.PropManager
}

// NewTraceSystem creates a new trace system
func NewTraceSystem(pm *propcore.PropManager) *TraceSystem {
	return &TraceSystem{
		traceLevel: TRACE_INFO,
		uiLogLines: 200,
		pm:         pm,
	}
}

// Start initializes the trace system
func (ts *TraceSystem) Start(cachePath, appName string) {
	if ts == nil {
		return
	}
	ts.traceMutex.Lock()
	defer ts.traceMutex.Unlock()

	if ts.traceStarted {
		return
	}

	if ts.traceLevel == 0 {
		ts.traceLevel = TRACE_INFO
	}

	logDir := filepath.Join(cachePath, "log")
	os.MkdirAll(logDir, 0777)

	// Rotate logfiles
	for i := 4; i >= 0; i-- {
		oldPath := filepath.Join(logDir, fmt.Sprintf("%s-%d.log", appName, i))
		newPath := filepath.Join(logDir, fmt.Sprintf("%s-%d.log", appName, i+1))
		os.Rename(oldPath, newPath)
	}

	logPath := filepath.Join(logDir, fmt.Sprintf("%s-0.log", appName))
	var err error
	ts.logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to open log file: %v", err)
		ts.logFile = nil
	} else {
		ts.logFile.WriteString("--MARK-- START\n")
	}

	ts.logStartTS = time.Now().UnixNano() / 1000000
	ts.traceStarted = true

	// Initialize property logging callback
	ts.SetupPropLogging()

	// Start property statistics if not already started
	if !ts.propStatsStarted {
		ts.propStatsStarted = true
		go ts.StartPropStats()
	}

	// Create logbuffer property under global
	// C: log_root = prop_create(prop_get_global(), "logbuffer")
	// (trace.c:339)
	globalProp := ts.pm.GetGlobal()
	if globalProp != nil {
		ts.logRoot = ts.pm.Create("logbuffer")
	}

	// C: trace.c:344-349 — the startup banner is emitted by the caller
	// (init.go) right after Start, where gconf fields are already populated.
}

// Fini shuts down the trace system
func (ts *TraceSystem) Fini() {
	if ts == nil {
		return
	}
	ts.traceMutex.Lock()
	defer ts.traceMutex.Unlock()

	if ts.logFile != nil {
		ts.logFile.WriteString("--MARK-- END\n")
		ts.logFile.Close()
		ts.logFile = nil
	}
	ts.traceStarted = false
}

// traceTmp — C: tracetmp_t (trace.c:44-48) — a queued log line that is
// turned into a prop child of log_root after trace_mutex is released.
type traceTmp struct {
	prefix  string
	message string
}

// Tracev logs a message with variable arguments
// C: tracev (trace.c:135-233).
func (ts *TraceSystem) Tracev(flags int, level int, subsys string, format string, ap ...any) {
	if ts == nil {
		return
	}
	if !ts.traceStarted {
		return
	}

	ts.traceMutex.Lock()

	var leveltxt string
	switch level {
	case TraceEmerg:
		leveltxt = "EMERG"
	case TRACE_ERROR:
		leveltxt = "ERROR"
	case TRACE_INFO:
		leveltxt = "INFO"
	case TRACE_DEBUG:
		leveltxt = "DEBUG"
	default:
		leveltxt = "?"
	}

	msg := fmt.Sprintf(format, ap...)

	prefix := fmt.Sprintf("%-15s [%-5s]:", subsys, leveltxt)

	var q []traceTmp // C: SIMPLEQ_HEAD tracetmp_queue

	// Split by newline and log each line (strsep equivalent)
	lines := strings.SplitSeq(msg, "\n")
	for line := range lines {
		if line == "" {
			continue // Skip empty lines
		}

		// Log to file
		if ts.logFile != nil {
			timestampMs := (time.Now().UnixNano()/1000000 - ts.logStartTS) / 1000
			timestamp := fmt.Sprintf("%02d:%02d:%02d.%03d: ",
				timestampMs/3600000,
				(timestampMs/60000)%60,
				(timestampMs/1000)%60,
				timestampMs%1000)

			ts.logFile.WriteString(timestamp + prefix + " " + line + "\n")
		}

		// Log to network
		if ts.netLogEnabled && ts.netLogConn != nil {
			ts.traceNet(level, prefix, line)
		}

		// Platform-specific logging (trace_arch equivalent)
		if ts.platformLogger != nil {
			ts.platformLogger(level, prefix, line)
		}

		// Log to stderr if level is high enough
		if level <= ts.traceLevel {
			log.Printf("%s %s", prefix, line)
		}

		// C: if(!(flags & TRACE_NO_PROP) && level != TRACE_EMERG)
		//    queue into tracetmp q; entries++
		if flags&TraceNoProp == 0 && level != TraceEmerg {
			q = append(q, traceTmp{prefix: prefix, message: line})
			ts.logEntries++
		}
	}

	// C: zapcnt = entries - UI_LOG_LINES when over; entries clamped
	// (trace.c:206-211) — computed under trace_mutex.
	zapcnt := 0
	if ts.logEntries > ts.uiLogLines {
		zapcnt = ts.logEntries - ts.uiLogLines
		ts.logEntries = ts.uiLogLines
	}

	ts.traceMutex.Unlock()

	// C: SIMPLEQ_FOREACH — prop_create_root + prop_set(prefix/message/
	// severity) + prop_set_parent(p, log_root); then
	// prop_destroy_first(log_root) × zapcnt (trace.c:214-232).
	if len(q) > 0 && ts.pm != nil && ts.logRoot != nil {
		for _, tt := range q {
			p := ts.pm.CreateRoot("")
			ts.pm.SetVEx(nil, p, "prefix", tt.prefix)
			ts.pm.SetVEx(nil, p, "message", tt.message)
			ts.pm.SetVEx(nil, p, "severity", leveltxt)
			p.SetParent(ts.logRoot)
		}
		for zapcnt > 0 {
			ts.pm.DestroyFirst(ts.logRoot)
			zapcnt--
		}
	}
}

// Trace logs a message with variable arguments
func (ts *TraceSystem) Trace(level int, subsys string, format string, ap ...any) {
	if ts == nil {
		return
	}
	ts.Tracev(0, level, subsys, format, ap...)
}

// Emerg logs an emergency message
func (ts *TraceSystem) Emerg(subsys string, format string, ap ...any) {
	if ts == nil {
		return
	}
	ts.Trace(TraceEmerg, subsys, format, ap...)
}

// Error logs an error message
func (ts *TraceSystem) Error(subsys string, format string, ap ...any) {
	if ts == nil {
		return
	}
	ts.Trace(TRACE_ERROR, subsys, format, ap...)
}

// Info logs an info message
func (ts *TraceSystem) Info(subsys string, format string, ap ...any) {
	if ts == nil {
		return
	}
	ts.Trace(TRACE_INFO, subsys, format, ap...)
}

// Debug logs a debug message
func (ts *TraceSystem) Debug(subsys string, format string, ap ...any) {
	if ts == nil {
		return
	}
	ts.Trace(TRACE_DEBUG, subsys, format, ap...)
}

// GetEnableMetadataDebug returns whether metadata debug is enabled
func (ts *TraceSystem) GetEnableMetadataDebug() bool {
	if ts == nil {
		return false
	}
	ts.traceMutex.Lock()
	defer ts.traceMutex.Unlock()
	return ts.enableMetadataDebug
}

// SetEnableMetadataDebug sets whether metadata debug is enabled
func (ts *TraceSystem) SetEnableMetadataDebug(enabled bool) {
	if ts == nil {
		return
	}
	ts.traceMutex.Lock()
	defer ts.traceMutex.Unlock()
	ts.enableMetadataDebug = enabled
}

// HexDump dumps data in hexadecimal format
func (ts *TraceSystem) HexDump(pfx string, data []byte) {
	if ts == nil {
		return
	}
	for i := 0; i < len(data); i += 16 {
		offset := fmt.Sprintf("0x%06x: ", i)
		var hexPart strings.Builder
		var asciiPart strings.Builder

		for j := 0; i+j < len(data) && j < 16; j++ {
			if j == 8 {
				hexPart.WriteString(" ")
			}
			hexPart.WriteString(fmt.Sprintf("%02x ", data[i+j]))
		}

		// Pad hex part
		for j := len(data) - i; j < 16; j++ {
			if j == 8 {
				hexPart.WriteString(" ")
			}
			hexPart.WriteString("   ")
		}

		for j := 0; i+j < len(data) && j < 16; j++ {
			b := data[i+j]
			if b < 32 || b > 126 {
				asciiPart.WriteString(".")
			} else {
				asciiPart.WriteString(string(b))
			}
		}

		ts.Debug(pfx, "%s%s %s", offset, hexPart.String(), asciiPart.String())
	}
}

// SetLevel sets the trace level
func (ts *TraceSystem) SetLevel(level int) {
	if ts == nil {
		return
	}
	ts.traceMutex.Lock()
	defer ts.traceMutex.Unlock()
	ts.traceLevel = level
}

// GetLevel returns the current trace level
func (ts *TraceSystem) GetLevel() int {
	if ts == nil {
		return TRACE_INFO
	}
	ts.traceMutex.Lock()
	defer ts.traceMutex.Unlock()
	return ts.traceLevel
}

// SetPlatformLogger sets the platform-specific logging callback (trace_arch equivalent)
func (ts *TraceSystem) SetPlatformLogger(logger func(level int, prefix string, message string)) {
	if ts == nil {
		return
	}
	ts.traceMutex.Lock()
	defer ts.traceMutex.Unlock()
	ts.platformLogger = logger
}

// SetNetLogConfig configures network logging
func (ts *TraceSystem) SetNetLogConfig(enabled bool, addr string, port int) {
	if ts == nil {
		return
	}
	ts.traceMutex.Lock()
	defer ts.traceMutex.Unlock()

	ts.netLogEnabled = enabled
	ts.netLogAddr = addr
	ts.netLogPort = port

	if ts.netLogConn != nil {
		ts.netLogConn.Close()
		ts.netLogConn = nil
	}

	if !enabled || addr == "" || port == 0 {
		return
	}

	serverAddr := fmt.Sprintf("%s:%d", addr, port)
	udpAddr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		log.Printf("Failed to resolve netlog server: %v", err)
		return
	}

	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		log.Printf("Failed to create netlog connection: %v", err)
		return
	}

	ts.netLogServer = udpAddr
	ts.netLogConn = conn
}

// traceNetRaw sends a raw message to the network log server
func (ts *TraceSystem) traceNetRaw(format string, ap ...any) {
	ts.traceMutex.Lock()
	defer ts.traceMutex.Unlock()

	if ts.netLogConn == nil || !ts.netLogEnabled {
		return
	}

	msg := fmt.Sprintf(format, ap...)
	_, err := ts.netLogConn.Write([]byte(msg))
	if err != nil {
		log.Printf("Failed to send netlog message: %v", err)
	}
}

// traceNet sends a formatted message to the network log server with color codes
func (ts *TraceSystem) traceNet(level int, prefix string, str string) {
	var sgr string

	switch level {
	case TraceEmerg:
		sgr = "\033[31m"
	case TRACE_ERROR:
		sgr = "\033[31m"
	case TRACE_INFO:
		sgr = "\033[33m"
	case TRACE_DEBUG:
		sgr = "\033[32m"
	default:
		sgr = "\033[35m"
	}

	ts.traceNetRaw("%s%s %s\033[0m\n", sgr, prefix, str)
}

// StartPropStats starts periodic property subscription statistics reporting
func (ts *TraceSystem) StartPropStats() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		pm := ts.pm
		if pm == nil {
			continue
		}

		numSubs, _ := pm.GetSubscriptionStats()
		ts.Info("PROP", "%d subs", numSubs)
	}
}

// SetupPropLogging initializes the logging callback in PropManager
func (ts *TraceSystem) SetupPropLogging() {
	pm := ts.pm
	if pm == nil {
		return
	}

	// Set the logging callback to use trace.Info
	pm.SetLogCallback(func(format string, args ...any) {
		ts.Info("PROP", format, args...)
	})
}
