#!/bin/sh
# Build freetype.wasm = FreeType 2.13.3 + fontconfig 2.15.0 + expat 2.8.3
# + zlib 1.3.1 + libpng 1.6.48 (PNG needed for CBDT color emoji) with
# wasi-sdk, emulated SjLj (wasm2go-compatible).
#
# Sources are vendored as tarballs under src/; generated config headers
# (fontconfig config.h, fcobjshash.h, expat_config.h) under geninc/;
# WASI header shims under stubinc/.
#
# Usage:  WASI_SDK=/path/to/wasi-sdk-27.0  ./build.sh
#         wasm2go -pkg ftwasm -o freetype_gen.go freetype.wasm
set -e
D=$(cd "$(dirname "$0")" && pwd)
WASI=${WASI_SDK:-/opt/wasi-sdk}
B=$D/build
W=$D/work
CC="$WASI/bin/clang --target=wasm32-wasip1 --sysroot=$WASI/share/wasi-sysroot"
CF="-O2 -DNDEBUG -I$D/stubinc -mllvm -enable-emscripten-sjlj"

mkdir -p $B/obj $W && cd $W

[ -d freetype-2.13.3 ]    || tar xf $D/src/freetype-2.13.3.tar.xz
[ -d fontconfig-2.15.0 ]  || tar xf $D/src/fontconfig-2.15.0.tar.xz
[ -d expat-2.8.3 ]        || tar xf $D/src/expat-2.8.3.tar.xz
[ -d zlib-1.3.1 ]         || tar xf $D/src/zlib-1.3.1.tar.gz
[ -d libpng-1.6.48 ]      || tar xf $D/src/libpng-1.6.48.tar.xz
FT=$W/freetype-2.13.3
FC=$W/fontconfig-2.15.0
EX=$W/expat-2.8.3
ZL=$W/zlib-1.3.1
LP=$W/libpng-1.6.48

# libpng needs pnglibconf.h — use the shipped prebuilt
[ -f $LP/pnglibconf.h ] || cp $LP/scripts/pnglibconf.h.prebuilt $LP/pnglibconf.h

FTCF="$CF -DFT2_BUILD_LIBRARY -I$FT/include -DFT_CONFIG_OPTION_USE_PNG -I$LP -I$ZL"
FCCF="$CF -DHAVE_CONFIG_H -I$D/geninc -I$FC -I$FC/src -I$FC/fontconfig -I$FT/include -I$EX/lib"
EXCF="$CF -DHAVE_EXPAT_CONFIG_H -I$D/geninc -I$EX/lib"

FTFILES="
src/base/ftbase.c src/base/ftdebug.c src/base/ftbbox.c src/base/ftbdf.c
src/base/ftbitmap.c src/base/ftcid.c src/base/ftfstype.c src/base/ftgasp.c
src/base/ftglyph.c src/base/ftgxval.c src/base/ftinit.c src/base/ftmm.c
src/base/ftotval.c src/base/ftpatent.c src/base/ftpfr.c src/base/ftstroke.c
src/base/ftsynth.c src/base/ftsystem.c src/base/fttype1.c src/base/ftwinfnt.c
src/autofit/autofit.c src/bdf/bdf.c src/cff/cff.c src/cid/type1cid.c
src/gzip/ftgzip.c src/lzw/ftlzw.c src/pcf/pcf.c src/pfr/pfr.c
src/psaux/psaux.c src/pshinter/pshinter.c src/psnames/psnames.c src/raster/raster.c src/sdf/sdf.c
src/sfnt/sfnt.c src/smooth/smooth.c src/svg/svg.c src/truetype/truetype.c
src/type1/type1.c src/type42/type42.c src/winfonts/winfnt.c
"
FCFILES="fcatomic.c fccache.c fccfg.c fccharset.c fccompat.c fcdbg.c
fcdefault.c fcdir.c fcformat.c fcfreetype.c fcfs.c fcptrlist.c fchash.c
fcinit.c fclang.c fclist.c fcmatch.c fcmatrix.c fcname.c fcobjs.c
fcpat.c fcrange.c fcserialize.c fcstat.c fcstr.c fcweight.c fcxml.c
ftglue.c"
EXFILES="xmlparse.c xmlrole.c xmltok.c random_getentropy.c"
ZLFILES="adler32.c compress.c crc32.c deflate.c gzclose.c gzlib.c
gzread.c gzwrite.c infback.c inffast.c inflate.c inftrees.c trees.c
uncompr.c zutil.c"
LPFILES="png.c pngerror.c pngget.c pngmem.c pngpread.c pngread.c
pngrio.c pngrtran.c pngrutil.c pngset.c pngtrans.c pngwio.c pngwrite.c
pngwtran.c pngwutil.c"

for f in $FTFILES; do
  o=$B/obj/ft_$(basename $f .c).o
  [ -f "$o" ] || $CC $FTCF -c $FT/$f -o $o
done
for f in $FCFILES; do
  $CC $FCCF -c $FC/src/$f -o $B/obj/fc_$(basename $f .c).o
done
for f in $EXFILES; do
  $CC $EXCF -c $EX/lib/$f -o $B/obj/ex_$(basename $f .c).o
done
for f in $ZLFILES; do
  o=$B/obj/zl_$(basename $f .c).o
  [ -f "$o" ] || $CC $CF -DZ_HAVE_UNISTD_H -c $ZL/$f -o $o
done
for f in $LPFILES; do
  o=$B/obj/lp_$(basename $f .c).o
  [ -f "$o" ] || $CC $CF -I$LP -I$ZL -c $LP/$f -o $o
done
$CC $FCCF -c $D/ftglue.c -o $B/obj/ftglue.o
$CC $FCCF -c $D/fcglue.c -o $B/obj/fcglue.o

$CC --target=wasm32-wasip1 --sysroot=$WASI/share/wasi-sysroot \
  -mexec-model=reactor -Wl,--no-entry \
  -Wl,--export=__stack_pointer -Wl,--export=__indirect_function_table \
  -D_WASI_EMULATED_GETPID -lwasi-emulated-getpid \
  $(grep -o 'ftw_[a-z_0-9]*\|fcw_[a-z_0-9]*' $D/ftglue.c $D/fcglue.c | cut -d: -f2 | sort -u | sed 's/^/-Wl,--export-if-defined=/' | tr '\n' ' ') \
  -Wl,--export-if-defined=malloc -Wl,--export-if-defined=free \
  -o $D/freetype.wasm $B/obj/*.o
ls -la $D/freetype.wasm

echo "now run: wasm2go -pkg ftwasm -o ../../../internal/ftwasm/freetype_gen.go freetype.wasm"
