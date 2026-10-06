//go:build windows

package arch

// Windows arch support — NO upstream C counterpart (upstream Movian
// never shipped a win32 arch; src/arch/win32 is referenced by
// threads.h for _MSC_VER but absent from the source drop). Modeled on
// the osx port's shapes: device id = MD5 of a machine-unique ID.

/*
#cgo LDFLAGS: -ladvapi32
#include <windows.h>

// MachineGuid — the Windows counterpart of IOPlatformUUID:
// HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid (stable per-OS-
// install machine identifier, same role upstream osx uses).
static int ml_machine_guid(char *out, size_t outlen) {
    DWORD type = 0, sz = (DWORD)outlen;
    LONG r = RegGetValueA(HKEY_LOCAL_MACHINE,
                          "SOFTWARE\\Microsoft\\Cryptography",
                          "MachineGuid", RRF_RT_REG_SZ, &type,
                          out, &sz);
    return r == ERROR_SUCCESS ? 0 : -1;
}
*/
import "C"

import (
	"crypto/md5"
	"encoding/hex"
	"unsafe"
)

// WindowsDeviceID — same shape as DarwinDeviceID (osx_app.m
// get_device_id): MD5 of the machine GUID, hex — feeds
// gconf.device_id.
func WindowsDeviceID() string {
	buf := make([]byte, 512)
	if C.ml_machine_guid((*C.char)(unsafe.Pointer(&buf[0])),
		C.size_t(len(buf))) != 0 {
		return ""
	}
	guid := C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
	digest := md5.Sum([]byte(guid))
	return hex.EncodeToString(digest[:])
}
