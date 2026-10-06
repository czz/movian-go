# FFmpeg per PS3 (psl1ght) — ricetta verificata 2026-10-05

Test eseguito con ps3toolchain installato in `/usr/local/ps3dev`
(`powerpc64-ps3-elf-gcc` 7.2.0, portlibs presenti, **libpsl1ght.a non
buildata** nel sistema di test).

## Risultato

**FFmpeg 6.1.2 compila pulito** → `libavcodec.a libavformat.a libavutil.a
libswresample.a libswscale.a` per PPU big-endian. E le sue header sono
compatibili con i binding Go di `internal/libav` + `internal/media/libav`
**senza modifiche**: le API usate dal port richiedono FFmpeg ≥ 5.1
(`AVChannelLayout`, `frame->ch_layout`, `ctx->ch_layout`,
`av_channel_layout_default/from_mask/describe/uninit`, opzioni swr
`in_chlayout/out_chlayout`) e non usano nulla di FFmpeg ≥ 7.
**FFmpeg ≤ 5.0 invece non basta** (nessun `ch_layout`, i campi
`channels`/`channel_layout` furono rimossi in FFmpeg 7 — il codice non
può usare i vecchi).

## Configure verificato

```sh
export PS3DEV=/usr/local/ps3dev
ffmpeg-6.1.2/configure \
  --prefix=$PWD/install \
  --cross-prefix=powerpc64-ps3-elf- \
  --arch=ppc64 --cpu=generic --target-os=none \
  --enable-cross-compile \
  --disable-shared --enable-static \
  --disable-programs --disable-doc --disable-avdevice --disable-avfilter \
  --disable-postproc --disable-network --disable-iconv \
  --disable-encoders --enable-encoder=ffvhuff --enable-encoder=pcm_s16le \
  --disable-debug \
  --extra-cflags="-I$PS3DEV/portlibs/ppu/include -O2" \
  --extra-ldflags="-L$PS3DEV/ppu/lib -L$PS3DEV/portlibs/ppu/lib"
make -j$(nproc) && make install
```

## Trappole trovate (tutte risolte dai flag sopra)

1. **`-Os` fa crashare il compilatore**: GCC 7.2 ppc64 va in ICE
   (`internal compiler error: in rs6000_savres_routine_name,
   config/rs6000/rs6000.c:28661`) su quasi ogni file. `--enable-small`
   aggiunge `-Os` → NON usarlo. `-O0`/`-O1`/`-O2` sono tutti OK.
2. **Niente socket**: newlib psl1ght non ha `sys/socket.h` →
   `--disable-network`. Innocuo: Movian non usa i protocolli nativi di
   FFmpeg — tutto l'I/O passa dal custom `AVIOContext`/`io_open` di
   fileaccess (vedi `patches/README.md`).
3. **`float_t` indefinito**: con `-std=c11` GCC definisce
   `FLT_EVAL_METHOD` (float.h) e il `math.h` di newlib skippa il typedef
   di `float_t`/`double_t`. Colpisce solo `aacenc.o` e `opusenc.o` →
   `--disable-encoders`, tenendo solo `ffvhuff` + `pcm_s16le` che bastano
   a `internal/ui/glw/glw_rec.go` (registrazione F12, muxer matroska —
   i muxer restano abilitati).
4. **Link di test di configure**: il toolchain specs auto-aggiunge
   `-lrt -llv2` → serve `-L$PS3DEV/ppu/lib` in `extra-ldflags`
   (è lì che vivono `librt.a`/`liblv2.a`).

## Muro successivo: non è FFmpeg, è Go

Avendo le lib a posto, il build `GOOS=linux GOARCH=ppc64` fallisce
prima ancora di avlib:

- `runtime/cgo` non compila su psl1ght: gcc non conosce `-pthread`
  (wrapper che lo filtra: facile), newlib non ha `clearenv()`, e il
  `pthread.h` psl1ght non espone `pthread_t`/`pthread_attr_t` POSIX
  (necessità di patch al runtime Go = port vero e proprio).
- `modernc.org/libc` (usato da kvstore/sqlite) non ha build constraints
  per `linux/ppc64` big-endian.
- `GOOS=linux GOARCH=ppc64` produce un ELF **Linux** (syscall Linux), non
  un self PSL1GHT — serve un port Go→psl1ght oppure Linux-on-PS3, dove
  il target linux/ppc64 + FFmpeg 9 funzionano nativamente.
- Il tag build `psl1ght` nel Makefile (`make ps3`) è nominale: nessun
  file Go lo usa, mancano i layer LV2 (video GCM, audio, pad, sysutil).
