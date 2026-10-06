//go:build windows

package arch

// Windows pipe for the asyncio wakeup channel — windows has no
// pollable anonymous pipe fds, so the canonical equivalent is a
// loopback TCP socketpair (what the C POSIX pipe() is used for here).
// NO upstream C counterpart (upstream never shipped win32).

/*
#cgo LDFLAGS: -lws2_32
#include <winsock2.h>
#include <ws2tcpip.h>

// loopback socketpair returning {accepted, connector} SOCKETs
static int ml_socketpair(SOCKET out[2]) {
    WSADATA wd;
    if (WSAStartup(MAKEWORD(2, 2), &wd) != 0)
        return -1;
    SOCKET l = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
    if (l == INVALID_SOCKET)
        return -1;
    struct sockaddr_in a;
    memset(&a, 0, sizeof(a));
    a.sin_family = AF_INET;
    a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    if (bind(l, (struct sockaddr *)&a, sizeof(a)) == SOCKET_ERROR ||
        listen(l, 1) == SOCKET_ERROR)
        { closesocket(l); return -1; }
    int alen = sizeof(a);
    if (getsockname(l, (struct sockaddr *)&a, &alen) == SOCKET_ERROR)
        { closesocket(l); return -1; }
    SOCKET c = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
    if (c == INVALID_SOCKET || connect(c, (struct sockaddr *)&a,
        sizeof(a)) == SOCKET_ERROR)
        { closesocket(l); return -1; }
    SOCKET s = accept(l, NULL, NULL);
    closesocket(l);
    if (s == INVALID_SOCKET)
        { closesocket(c); return -1; }
    out[0] = s;
    out[1] = c;
    return 0;
}
*/
import "C"

import "syscall"

// Pipe — socketpair() equivalent; both ends are Winsock SOCKETs
// (nonblocking set by the caller via sysSetNonblock / ioctlsocket).
func Pipe() ([]int, error) {
	var out [2]C.SOCKET
	if C.ml_socketpair(&out[0]) != 0 {
		return nil, syscall.EINVAL
	}
	return []int{int(uintptr(out[0])), int(uintptr(out[1]))}, nil
}
