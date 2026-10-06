#!/bin/sh
# Build dvd.wasm = vendored ext/dvd (libdvdcss + libdvdread + libdvdnav
# from internal/backend/dvd/dvdlib) with wasi-sdk, emulated SjLj
# (wasm2go-compatible). -DWII selects the vendored no-ioctl stubs
# (real-device ioctls don't exist under WASI; svfs/vfs input is
# unaffected — all I/O goes through the fa_* host imports anyway).
#
# Usage:  WASI_SDK=/path/to/wasi-sdk-27.0  ./build.sh
#         wasm2go -pkg dvdwasm -o dvd_gen.go dvd.wasm
set -e
D=$(cd "$(dirname "$0")" && pwd)
SRC=$(cd "$D/../csrc" && pwd)
WASI=${WASI_SDK:-/opt/wasi-sdk}
B=$D/build
CC="$WASI/bin/clang --target=wasm32-wasip1 --sysroot=$WASI/share/wasi-sysroot"
CF="-O2 -DNDEBUG -DWII -I$SRC -I$SRC/dvdnav -I$SRC/dvdnav/vm -I$SRC/libdvdread -I$SRC/dvdcss -DDVDNAV_COMPILE -D_GNU_SOURCE -DHAVE_CONFIG_H -DHAVE_DVDCSS_DVDCSS_H -DHAVE_LIMITS_H -DHAVE_UNISTD_H -DHAVE_ERRNO_H -D_LARGEFILE_SOURCE -D_LARGEFILE64_SOURCE -Wno-strict-aliasing -Wno-implicit-function-declaration -w -mllvm -enable-emscripten-sjlj"

mkdir -p $B/obj && cd $SRC
for f in *.c; do
  o=$B/obj/dv_$(basename $f .c).o
  [ -f "$o" ] || $CC $CF -c $SRC/$f -o $o
done
$CC $CF -c $D/dvglue.c -o $B/obj/dvglue.o

$CC --target=wasm32-wasip1 --sysroot=$WASI/share/wasi-sysroot \
  -mexec-model=reactor -Wl,--no-entry \
  -Wl,--export=__stack_pointer -Wl,--export=__indirect_function_table \
  $(grep -o 'dvw_[a-z_0-9]*' $D/dvglue.c | sort -u | sed 's/^/-Wl,--export-if-defined=/' | tr '\n' ' ') \
  -Wl,--export-if-defined=malloc -Wl,--export-if-defined=free \
  -o $D/dvd.wasm $B/obj/*.o
ls -la $D/dvd.wasm
