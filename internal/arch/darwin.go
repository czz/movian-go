//go:build darwin

package arch

// Canonical port of src/arch/darwin.c — the mach host_statistics /
// host_processor_info CPU+memory monitor feeding $global.system
// (cpuinfo.available/cpus/cpuN.load, mem.systotal/sysfree/activeMem)
// on a 1s timer, plus the darwin-relevant parts of
// src/arch/osx/osx_app.m: arch_get_avtime (host time → µs),
// get_device_id (IOPlatformUUID → MD5 → hex) and
// get_system_concurrency (sysctl HW_NCPU — GetSystemConcurrency's
// NumCPU is the equivalent). osx_misc.c's arch_malloc_size stays a
// Go-side no-op (arch.go MallocSize — Go can't size C allocations).

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <stdlib.h>
#include <string.h>
#include <sys/sysctl.h>
#include <mach/mach.h>
#include <mach/task.h>
#include <mach/processor_info.h>
#include <mach/mach_host.h>
#include <mach/host_info.h>
#include <mach/task_info.h>
#include <mach/mach_time.h>
#include <IOKit/IOKitLib.h>
#include <CoreFoundation/CoreFoundation.h>

// C: host_processor_info(PROCESSOR_CPU_LOAD_INFO) walk in
// cpu_monitor_do (darwin.c:87-127). Fills caller arrays, frees the
// kernel buffer like C's vm_deallocate.
static int ml_cpu_load_info(unsigned int *ncpu,
                            unsigned long *totals, unsigned long *idles) {
  processor_info_t pinfo;
  mach_msg_type_number_t msg_count;
  unsigned int cpu_count;
  if (host_processor_info(mach_host_self(), PROCESSOR_CPU_LOAD_INFO,
                          &cpu_count, (processor_info_array_t *)&pinfo,
                          &msg_count) != KERN_SUCCESS)
    return -1;
  *ncpu = cpu_count;
  for (unsigned int i = 0; i < cpu_count; i++) {
    processor_info_t pi = pinfo + (CPU_STATE_MAX * i);
    totals[i] = (unsigned long)(pi[CPU_STATE_USER] + pi[CPU_STATE_SYSTEM] +
                                pi[CPU_STATE_NICE] + pi[CPU_STATE_IDLE]);
    idles[i] = (unsigned long)pi[CPU_STATE_IDLE];
  }
  vm_deallocate(mach_task_self(), (vm_address_t)pinfo,
                (vm_size_t)sizeof(*pinfo) * msg_count);
  return 0;
}

// C: mem_monitor_do (darwin.c:44-79) — sysctl HW_PAGESIZE +
// host_statistics(HOST_VM_INFO) + task_info(TASK_BASIC_INFO_64).
static int ml_mem_info(int *pagesize, unsigned int *free_cnt,
                       unsigned int *total_cnt,
                       unsigned long long *resident) {
  int mib[2] = {CTL_HW, HW_PAGESIZE};
  size_t len = sizeof(*pagesize);
  if (sysctl(mib, 2, pagesize, &len, NULL, 0) < 0)
    return -1;
  vm_statistics_data_t vmstat;
  mach_msg_type_number_t count = HOST_VM_INFO_COUNT;
  if (host_statistics(mach_host_self(), HOST_VM_INFO,
                      (host_info_t)&vmstat, &count) != KERN_SUCCESS)
    return -1;
  *total_cnt = vmstat.wire_count + vmstat.active_count +
               vmstat.inactive_count + vmstat.free_count;
  *free_cnt = vmstat.free_count;
  task_basic_info_64_data_t info;
  unsigned size = sizeof(info);
  task_info(mach_task_self(), TASK_BASIC_INFO_64, (task_info_t)&info, &size);
  *resident = info.resident_size;
  return 0;
}

// C: arch_get_avtime (osx_app.m:50-53) —
// AudioConvertHostTimeToNanos(AudioGetCurrentHostTime()) / 1000.
static int64_t ml_avtime_us(void) {
  static mach_timebase_info_data_t tb;
  if (tb.denom == 0)
    mach_timebase_info(&tb);
  return (int64_t)(mach_absolute_time() * tb.numer / tb.denom / 1000);
}

// C: get_device_id (osx_app.m:72-90) — IOPlatformUUID string.
static int ml_platform_uuid(char *out, size_t outlen) {
  io_registry_entry_t root =
    IORegistryEntryFromPath(kIOMasterPortDefault, "IOService:/");
  CFTypeRef uuid = IORegistryEntryCreateCFProperty(
    root, CFSTR(kIOPlatformUUIDKey), kCFAllocatorDefault, 0);
  IOObjectRelease(root);
  if (uuid == NULL)
    return -1;
  Boolean ok = CFStringGetCString((CFStringRef)uuid, out, (CFIndex)outlen,
                                  kCFStringEncodingMacRoman);
  CFRelease(uuid);
  return ok ? 0 : -1;
}
*/
import "C"

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
	"unsafe"

	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// GetAvtime — C: arch_get_avtime (osx_app.m:50-53).
func GetAvtime() int64 {
	return int64(C.ml_avtime_us())
}

// DarwinDeviceID — C: get_device_id (osx_app.m:72-90): MD5 of
// IOPlatformUUID, hex — feeds gconf.device_id.
func DarwinDeviceID() string {
	buf := make([]byte, 512)
	if C.ml_platform_uuid((*C.char)(unsafe.Pointer(&buf[0])),
		C.size_t(len(buf))) != 0 {
		return ""
	}
	uuid := C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
	digest := md5.Sum([]byte(uuid))
	return hex.EncodeToString(digest[:])
}

// darwinCpuMonitor — C: darwin.c statics (p_sys, p_cpuroot, p_cpu[],
// p_load[], last_total[], last_idle[], cpu_count, timer).
type darwinCpuMonitor struct {
	propMgr   *propcore.PropManager
	pSys      *propcore.Prop // C: p_sys — "system" under global root
	pCpuRoot  *propcore.Prop // C: p_cpuroot — cpuinfo/cpus
	pCpu      []*propcore.Prop
	pLoad     []*propcore.Prop
	lastTotal []uint64
	lastIdle  []uint64
	cpuCount  int

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// C: mem_monitor_do (darwin.c:44-79).
func (m *darwinCpuMonitor) memMonitorDo() {
	var pagesize C.int
	var freeCnt, totalCnt C.uint
	var resident C.ulonglong
	if C.ml_mem_info(&pagesize, &freeCnt, &totalCnt, &resident) != 0 {
		return
	}
	ps := int64(pagesize)

	// C: mem = prop_create(p_sys, "mem") + prop_set(..., PROP_SET_INT,...)
	mem := m.propMgr.CreateEx(m.pSys, "mem", nil, false, false)
	m.propMgr.CreateInt(mem, "systotal", int(int64(totalCnt)/1024*ps))
	m.propMgr.CreateInt(mem, "sysfree", int(int64(freeCnt)/1024*ps))
	m.propMgr.CreateInt(mem, "activeMem", int(int64(resident)/1024))
}

// C: cpu_monitor_do (darwin.c:83-129).
func (m *darwinCpuMonitor) cpuMonitorDo() {
	maxCPU := m.cpuCount
	if maxCPU == 0 {
		maxCPU = 64
	}
	totals := make([]C.ulong, maxCPU)
	idles := make([]C.ulong, maxCPU)
	var ncpu C.uint
	if C.ml_cpu_load_info(&ncpu, &totals[0], &idles[0]) != 0 {
		return
	}
	// C: if(cpu_count != cpu_count_temp) { dealloc; return; }
	if m.cpuCount != int(ncpu) {
		return
	}

	for i := 0; i < m.cpuCount; i++ {
		// C: if(p_cpu[i] == NULL) { prop_create + name + load }
		if m.pCpu[i] == nil {
			m.pCpu[i] = m.propMgr.CreateEx(m.pCpuRoot, "", nil, false, false)
			m.propMgr.CreateString(m.pCpu[i], "name",
				fmt.Sprintf("CPU%d", i))
			m.pLoad[i] = m.propMgr.CreateEx(m.pCpu[i], "load", nil, false, false)
		}

		total := uint64(totals[i])
		idle := uint64(idles[i])
		di := idle - m.lastIdle[i]
		dt := total - m.lastTotal[i]
		m.lastIdle[i] = idle
		m.lastTotal[i] = total

		// C: prop_set_float(p_load[i], 1.0 - di/dt)
		var load float32
		if dt != 0 {
			load = 1.0 - float32(di)/float32(dt)
		}
		m.pLoad[i].SetFloat(load)
	}
}

// C: timercb (darwin.c:131-137) — callout re-armed at 1s.
func (m *darwinCpuMonitor) run() {
	defer m.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.cpuMonitorDo()
			m.memMonitorDo()
		}
	}
}

// DarwinStartCpuMonitor — C: darwin_init_cpu_monitor (darwin.c:140-181),
// registered as INITME(INIT_GROUP_API) upstream.
func DarwinStartCpuMonitor(pm *propcore.PropManager) {
	m := &darwinCpuMonitor{propMgr: pm}

	// C: p_sys = prop_create(prop_get_global(), "system")
	m.pSys = pm.CreateEx(pm.GetGlobal(), "system", nil, false, false)

	// C: host_processor_info first call → cpu_count (darwin.c:146-156)
	maxCPU := 256
	totals := make([]C.ulong, maxCPU)
	idles := make([]C.ulong, maxCPU)
	var ncpu C.uint
	if C.ml_cpu_load_info(&ncpu, &totals[0], &idles[0]) != 0 {
		archTS.ts.Trace(tracepkg.TRACE_ERROR, "darwin",
			"host_processor_info(PROCESSOR_CPU_LOAD_INFO) failed")
		return
	}
	m.cpuCount = int(ncpu)
	m.pCpu = make([]*propcore.Prop, m.cpuCount)
	m.pLoad = make([]*propcore.Prop, m.cpuCount)
	m.lastTotal = make([]uint64, m.cpuCount)
	m.lastIdle = make([]uint64, m.cpuCount)

	// C: prop_set_int(prop_create(prop_create(p_sys,"cpuinfo"),"available"),1)
	cpuInfo := pm.CreateEx(m.pSys, "cpuinfo", nil, false, false)
	pm.CreateInt(cpuInfo, "available", 1)
	m.pCpuRoot = pm.CreateEx(cpuInfo, "cpus", nil, false, false)

	// C: cpu_monitor_do(); mem_monitor_do(); callout_arm(1s)
	m.cpuMonitorDo()
	m.memMonitorDo()
	m.stopCh = make(chan struct{})
	m.wg.Add(1)
	go m.run()
}
