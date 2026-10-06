# freetype.wasm — unified FreeType + Fontconfig + Expat for wasm2go

`freetype.wasm` is the single artifact behind the default pure-Go font
stack (`internal/ftwasm`). It is compiled once for wasm32-wasip1 and
translated to Go with `wasm2go`, so it runs identically on every
GOOS/GOARCH Go supports.

## Contents

| Component | Version | License | Source |
|---|---|---|---|
| FreeType | 2.13.3 | FTL / GPLv2 (dual, upstream default) | `src/freetype-2.13.3.tar.xz` |
| Fontconfig | 2.15.0 | MIT-style (upstream COPYING) | `src/fontconfig-2.15.0.tar.xz` |
| Expat | 2.8.3 | MIT | `src/expat-2.8.3.tar.xz` |
| wasi-libc | (wasi-sdk 27.0) | Apache-2.0 WITH LLVM-exception | toolchain, linked in |

Glue sources (`ftglue.c`, `fcglue.c`) are part of this repository and
export the `ftw_*` / `fcw_*` seam functions used by
`internal/text` and `internal/image`.

Generated/config headers live in `geninc/`:
`config.h` (fontconfig), `fcobjshash.h` + `fcobjshash.gperf`
(pre-generated with gperf), `expat_config.h`.
WASI shims live in `stubinc/`: `setjmp.h` (emscripten-SjLj emulation,
FreeType ftgrays/sfnt validators) and `fcntl.h` (file-locking constants
missing from wasi-libc).

## Rebuild

```sh
WASI_SDK=/path/to/wasi-sdk-27.0 ./build.sh          # -> freetype.wasm
wasm2go -pkg ftwasm -o ../../../internal/ftwasm/freetype_gen.go freetype.wasm
```

Notes:

- `-mllvm -enable-emscripten-sjlj` emulates setjmp/longjmp without
  wasm EH opcodes (wasm2go does not support EH); `longjmp` becomes a
  host `panic` recovered at `invoke_*` boundaries.
- `--export=__stack_pointer` / `--export=__indirect_function_table`
  are required by `internal/ftwasm/host.go` for the SjLj bookkeeping.
- The `FcFontSort`/pattern surface used by `internal/text/fontconfig.go`
  is wrapped by `fcglue.c` (`fcw_*`); filesystem access reaches the host
  through the WASI implementation in `internal/ftwasm/fs.go`.
