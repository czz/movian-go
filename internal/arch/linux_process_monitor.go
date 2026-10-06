//go:build linux

package arch

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	propcore "github.com/czz/movian-go/internal/prop"
)

// ProcessMonitor monitors system resources (CPU, memory)
type ProcessMonitor struct {
	systemProp  *propcore.Prop
	cpuRootProp *propcore.Prop
	cpuProps    []*propcore.Prop
	loadProps   []*propcore.Prop
	lastIdle    []int64
	lastTotal   []int64
	isValid     bool
	ticker      *time.Ticker
	stopChan    chan struct{}
	mu          sync.Mutex
	propMgr     *propcore.PropManager
}

// NewProcessMonitor creates a new process monitor
func NewProcessMonitor(systemProp *propcore.Prop, propMgr *propcore.PropManager) *ProcessMonitor {
	pm := &ProcessMonitor{
		systemProp: systemProp,
		stopChan:   make(chan struct{}),
		lastIdle:   make([]int64, 17),
		lastTotal:  make([]int64, 17),
		cpuProps:   make([]*propcore.Prop, 17),
		loadProps:  make([]*propcore.Prop, 16),
		propMgr:    propMgr,
	}

	// C: p_cpuroot = prop_create(prop_create(p_sys, "cpuinfo"), "cpus")
	cpuInfoProp := pm.propMgr.CreateEx(systemProp, "cpuinfo", nil, false, false)
	pm.cpuRootProp = pm.propMgr.CreateEx(cpuInfoProp, "cpus", nil, false, false)

	return pm
}

// Start begins monitoring
func (pm *ProcessMonitor) Start() {
	pm.mu.Lock()
	if pm.ticker != nil {
		pm.mu.Unlock()
		return // Already running
	}
	pm.mu.Unlock()

	// Initial update
	pm.updateCPU()
	pm.updateMemory()

	// Start periodic updates
	pm.mu.Lock()
	pm.ticker = time.NewTicker(1 * time.Second)
	pm.mu.Unlock()
	go func() {
		for {
			select {
			case <-pm.ticker.C:
				pm.updateCPU()
				pm.updateMemory()
			case <-pm.stopChan:
				return
			}
		}
	}()
}

// Stop stops monitoring
func (pm *ProcessMonitor) Stop() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.ticker != nil {
		pm.ticker.Stop()
		pm.ticker = nil
		close(pm.stopChan)
		pm.stopChan = make(chan struct{})
	}
}

// updateCPU reads /proc/stat and updates CPU usage
func (pm *ProcessMonitor) updateCPU() {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}

		if !strings.HasPrefix(fields[0], "cpu") {
			continue
		}

		// Parse CPU values: user, nice, system, idle, iowait, irq, softirq
		var values [7]uint64
		for i := range 7 {
			val, err := strconv.ParseUint(fields[i+1], 10, 64)
			if err != nil {
				continue
			}
			values[i] = val
		}

		total := int64(values[0] + values[1] + values[2] + values[3] + values[4] + values[5] + values[6])
		idle := int64(values[3])

		// Determine CPU ID
		var id int
		if fields[0] == "cpu" {
			id = 16 // Aggregate
		} else {
			idStr := fields[0][3:]
			id, err = strconv.Atoi(idStr)
			if err != nil || id < 0 || id > 16 {
				continue
			}
		}

		pm.mu.Lock()
		if pm.isValid {
			deltaIdle := idle - pm.lastIdle[id]
			deltaTotal := total - pm.lastTotal[id]

			if id < 16 {
				// C: if(p_cpu[id] == NULL) {
				//   p_cpu[id] = prop_create(p_cpuroot, NULL);  // anonymous
				//   prop_set(p_cpu[id], "name", PROP_SET_STRING, "CPU%d");
				//   p_load[id] = prop_create(p_cpu[id], "load"); }
				if pm.cpuProps[id] == nil {
					cpuProp := pm.propMgr.CreateEx(pm.cpuRootProp, "", nil, false, false)
					pm.cpuProps[id] = cpuProp
					pm.propMgr.CreateString(cpuProp, "name", fmt.Sprintf("CPU%d", id))
					pm.loadProps[id] = pm.propMgr.CreateEx(cpuProp, "load", nil, false, false)
				}

				var load float64
				if deltaTotal > 0 {
					load = 1.0 - (float64(deltaIdle) / float64(deltaTotal))
				}
				if load < 0 {
					load = 0
				} else if load > 1 {
					load = 1
				}

				pm.loadProps[id].SetFloat(float32(load))
			}
		}
		pm.lastIdle[id] = idle
		pm.lastTotal[id] = total
		pm.isValid = true
		pm.mu.Unlock()
	}

	// C: prop_set_int(prop_create(prop_create(p_sys, "cpuinfo"),
	//                            "available"), 1);
	cpuInfoProp := pm.propMgr.CreateEx(pm.systemProp, "cpuinfo", nil, false, false)
	pm.propMgr.CreateInt(cpuInfoProp, "available", 1)
}

// updateMemory — C: meminfo_do (linux_process_monitor.c:108-152).
// Application counters come from mallinfo() (cgo — the real glibc
// allocator values, not runtime.MemStats), system memory from
// /proc/meminfo MemTotal/MemFree only.
func (pm *ProcessMonitor) updateMemory() {
	memProp := pm.propMgr.CreateEx(pm.systemProp, "mem", nil, false, false)

	// C: struct mallinfo mi = mallinfo();
	mi := GetMallInfo()
	pm.propMgr.CreateInt(memProp, "arena", (mi.Hblks+mi.Arena)/1024)
	pm.propMgr.CreateInt(memProp, "unusedChunks", mi.Ordblks)
	pm.propMgr.CreateInt(memProp, "activeMem", mi.Uordblks/1024)
	pm.propMgr.CreateInt(memProp, "inactiveMem", mi.Fordblks/1024)

	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		name := fields[0]
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}

		switch name {
		case "MemTotal:":
			pm.propMgr.CreateInt(memProp, "systotal", int(value))
		case "MemFree:":
			pm.propMgr.CreateInt(memProp, "sysfree", int(value))
		}
	}
}
