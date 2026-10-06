# FFmpeg Boundary — Ownership & Layering (Fase 7.1)

Audit del confine C/Go per FFmpeg. Due livelli, confermati dall'inventario import:

| Layer | Package | Domanda a cui risponde |
|---|---|---|
| Basso | `internal/libav` | wrapper cgo su libffmpeg: alloc/free, parse, packet, frame. Zero dipendenze di dominio (l'import `fileaccesscore` è solo nei test; l'I/O passa dall'interfaccia `FileAccessReader` + shim C `fa_avio_*`). |
| Alto | `internal/media/libav` | adapter Movian: demuxer/decoder, hw accel (vaapi/vdpau/cuda/mediacodec), subtitle, imagethumb. Importa `media/core`, `video`, `drm`, `image`, `trace`. |

## Ownership per tipo

| Tipo Go | Allocato da | Liberato da | Refcount FFmpeg | Uscita dal package | Thread |
|---|---|---|---|---|---|
| `*AvParser` | `NewAvParser` (`av_parser_init`) | `(*AvParser).Close` (`av_parser_close`, nil-safe) | no | sì, come puntatore opaco — il `cPtr` non esce (`Ptr()` solo per seam cgo legacy) | un chiamante alla volta (parser stateful, come in C) |
| `*AVCodecContext` | `AvcodecAllocContext3` / `WrapAVCodecContext` (per ctx allocati da shim C) | `AvcodecFreeContext` (nil-safe, azzera `cPtr`) / `AvcodecFreeContextRaw` per i raw residui | no | `MediaCodec.Ctx` è ora `*AVCodecContext` tipizzato; `FmtCtx` è `*AVCodecContext` tipizzato (audit: tutti i writer passano AVCodecContext o nil; il param `opaque` di `MediaCodecCreate` resta `unsafe.Pointer` C-parity, wrappato all'assegnazione) | singolo decoder thread |
| `*AVFormatContext` | `AvformatOpenInput`/`AvformatAllocContext` | `AvformatCloseInput`/`AvformatFreeContext` | no | sì (`AVFormatCtx` in media/libav, `FmtCtx` in mediacore) | demuxer thread |
| `*AVPacket` | `AvPacketAlloc` / `av_read_frame` | `AvPacketFree` (`ml_av_packet_free`) / `AvPacketUnref` | **sì** — `buf` refcounted; `AvPacketRef`/`Unref` preservati | i dati escono come `MediaBuf` (copia gestita dal core, come C) | producer → consumer tramite mb |
| `*AVFrame` | `AvFrameAlloc` / decoder | `AvFrameFree` (`ml_av_frame_free`) / `AvFrameUnref` | **sì** — `buf[]` refcounted; `av_frame_move_ref` usato per zero-copy verso video pipeline | `AVFrame` esce verso `internal/video` per display | decoder → video thread via queue |
| `*AVIOContext` | `fa_avio_open` (shim C su `FileAccessReader`) | `c_free_avio_context` / `fa_avio_free` | no | resta dentro `AVFormatContext.pb` | demuxer thread; read-pump con timeout (stalled HTTP non blocca altri demuxer) |
| `*SwrContext` | `SwrAlloc`/`SwrInit` | `SwrFree` | no | no | audio decoder thread |
| `*BSFContext` | `AvBsfAlloc`/`AvBsfInit` | `AvBsfFree` | no | no | bitstream filter nel demux path |

## Struttura file `internal/libav` (split Fase 7)

`libav_cgo.go` era monolitico (~1590 righe, 102 decl) ed è stato diviso per dominio, in parallelo ai file C upstream (`libav.c`, `fa_libav.c`). Solo spostamento di codice — superficie dei simboli identica (verificata con diff prima/dopo), zero modifiche comportamentali.

| File | Contenuto | C upstream |
|---|---|---|
| `libav_cgo.go` | preamble residuo, `LibAVSystem` + `SetupLibAV`, `AVRational`/consts, `AvMalloc`/`AvFreep`, `avGetPixFmtName` | `libav.c` |
| `avio.go` | `AVIOContext`, `FileAccessReader`/`FileAccessHandle`, callback `//export goRead/goSeekCallback`, `FALibavReopen`/`Close`/`CloseAVIO`, `AvioSize`, `FALibavGetStrategyForFile` | `fa_libav.c` (avio part) |
| `fa_libav.go` | `FALibavOpenFormat`/`OpenStrategyAudio`/`CloseFormat`, `mimetype2fmt`, `faLibavOpenError`, `FALibavErrorToTxt`, `ErrorToStr`, strategy consts | `fa_libav.c` |
| `avcodec_ctx.go` | `AVCodec`, `AVCodecContext` + metodi, `Avcodec*`, `WrapAVCodecContext`, send/receive/flush, profile/channel helpers | `libavcodec` wrappers |
| `avformat_ctx.go` | `AVFormatContext`, `AVChapter`, `AVStream`, open/close/find_stream_info/read/seek, `StreamCodecCtx`, `AvFormatCtxStreamCodecCtx` | `libavformat` wrappers |
| `avpacket.go` | `AVPacket` + alloc/ref/unref/free + field accessors | `libavcodec/packet` wrappers |
| `avframe.go` | `AVFrame` + alloc/free/unref + `AvImageAlloc` + setters | `libavutil/frame` wrappers |
| `swscale.go` | `SwsContext` + `SwsGet/Cached/Scale/Free` | `libswscale` wrappers |

Ogni file ha il proprio preamble cgo con sole le include/dichiarazioni che usa. Regola rispettata: `avio.go` (che ospita `//export`) ha preamble **solo-dichiarazioni**. Gli helper `static ml_*` sono stati spostati ciascuno nel file che li usa (internal linkage → nessun simbolo duplicato). Dichiarazioni `extern goReadCallback`/`goSeekCallback` restano in `avio.go`; gli shim `c_*`/`fa_*` di `libav_avio.c` sono dichiarati nel file che li invoca (`avio.go` ↔ `fa_libav.go`).

## Regole trasversali

- **Durata callback**: le callback cgo (`fa_avio_read`, seek, interrupt) devono tornare — nessun puntatore Go registrato oltre la chiamata (rispetta cgo pointer rules).
- **Errori**: `Averror(int)` converte i codici negativi FFmpeg; i wrapper non tracciano errno.
- **Shim per double-pointer**: `avcodec_free_context`/`av_frame_free`/`av_packet_free` vogliono `T**` — shim C `ml_*_free(void*)` evita di esporre `&wrapper.cPtr` e runtime pinning.
- **Ctx tipizzato** (questa migrazione): `MediaCodec.Ctx` è `*libav.AVCodecContext` — i soli writer sono i path lavc (`cgo.go`, `audio.go`); i backend hw non lo popolano. I call site usano `.CPtr()` (nil-safe); le free usano `AvcodecFreeContext` + `Ctx = nil` (= C `avcodec_free_context(&ctx)`).
- **ATTENZIONE — conversione implicita**: `*T` → `unsafe.Pointer` è implicita in Go e passa l'indirizzo del **wrapper Go**, non `cPtr`. Ogni passaggio di un wrapper tipizzato a un parametro `unsafe.Pointer` richiede `.CPtr()` esplicito — il compilatore NON segnala l'errore (compila e corrompe il puntatore a runtime).
- **CodecCtxFromStream tipizzato**: ritorna `*libav.AVCodecContext`; `FreeCodecCtx` lo prende tipizzato (coppia lifecycle). I consumatori nel layer alto passano `.CPtr()` ai helper raw `Cctx*`/`Thumbctx*` (plumbing cgo interno dell'adapter).
- **`Cctx*`/`Thumbctx*`/`Sws*` tipizzati** (incremento 5): gli helper di `imagethumb.go` prendono `*libav.AVCodecContext` / `*libav.SwsContext` (estrazione nil-safe via `cctxPtr`/`swsPtr`); `ThumbctxAlloc` ritorna `WrapAVCodecContext`; `SwsGet` ritorna `WrapSwsContext` (nuovo seam in internal/libav, stesso pattern di `WrapAVCodecContext`). `fa_imageloader`: `il.ctx` e `il.thumbCtx` ora `*libav.AVCodecContext` — nessun `.CPtr()` residuo nei call site. `ReadPacketRaw` ritorna `*libav.AVPacket` tipizzato (incremento 6): ownership trasferita al chiamante, free via `libav.AvPacketFree`.
- **Parser tipizzato** (questa migrazione): `MediaCodec.ParserCtx` è `*libav.AvParser`, non più `unsafe.Pointer`. HLS reset = `Close()` + `NewAvParser`. DVD/RTMP/vobsub usano metodi. `Parse2`/`Parse2Pos` prendono `*AVCodecContext`; `Parse2Ptr` eliminato (FmtCtx ora tipizzato).
- **Famiglia audio codec tipizzata** (incremento 7): `AudioCodecOpen` ritorna `*AVCodecContext` (ownership al chiamante — `mc.Ctx = ctx` diretto, eliminato il doppio wrap); `AudioFrameDecode`/`AudioFrameDecodePkt`/`AudioCodecCtxSampleRate`/`AudioCodecCtxChannels`/`MqSetMeta` prendono `*AVCodecContext`. `AvPacketFromInfo` ritorna `*AVPacket`; `AvPacketFree`, `LibAVAudioDecode`, `PacketEncryption*`/`PacketData`/`DRMDecryptPacket` prendono `*AVPacket`. Gli shim `C.ml_*`/`C.decode_audio_*` continuano a ricevere `.CPtr()` al loro interno.
- **`WrapFormatCtx`/`MetadataFromCodec` tipizzati** (incremento 9): `WrapFormatCtx(*libav.AVFormatContext)` (era `unsafe.Pointer` — elimina `.CPtr()` ai 5 call site, borrow non-owning); `MetadataFromCodec(*libav.AVCodecContext)`. Dead code rimosso in incremento 13: `CopyCodecParams`, `GetRawContext`.
- **`MediaFormat.FCtx` tipizzato** (incremento 10): `*libav.AVFormatContext` (audit: campo mai letto — solo scritto in create/deref; `mf->fctx` in C serve al codec open ma il port non lo legge). `MediaFormatCreate` param tipizzato; nuovo seam `libav.WrapAVFormatContext` + accessor `AVFormatCtx.LibavCtx()`. fa_video/fa_audio/icecast passano `fctxLibav` diretto; playback_* usano `LibavCtx()`.
- **Famiglia frame/packet adapter tipizzata** (incremento 11): tutti gli helper `cgo.go` (`avFrame*`, `avPicture*`, `AvFrame*Raw`, `avPacket*`, `avNewPacket`, `avcodecSendPacket`/`ReceiveFrame`/`FlushBuffers`, `avCodecCtx*`) prendono `*AVFrame`/`*AVPacket`/`*AVCodecContext` — `.CPtr()` solo dentro le call C. `vdFrame`/`LibAVDeliverFrame` tipizzati; `vd.Frame`/`vd.Convert` erano già `*AVFrame`. `FrameInfo.AVFrame` (C `fi_avframe`) → `*AVFrame` (borrow al renderer). `HwframeTransfer`/`Ml*BindCodec`/`CtxIsVdpau`/`Vaapi{ExportSurface,AllocFrame,FillFrame}` tipizzati **in parallelo su tutte le varianti build-tag** (vaapi/cuda/vdpau/d3d_windows + off stubs). Seam nuovo `libav.WrapAVFrame`. `s.gvsRefAux`/`gvsRefRelease` in glw restano raw (seam renderer): `.CPtr()` out / `WrapAVFrame` in. Dead code rimosso in incremento 13: `medialibav.AvcodecFreeContext(*unsafe.Pointer)`, `CtxIsVdpau` + helper C `ml_vdpau_ctx_is_vdpau`.
- **`MediaBuf.Pkt` tipizzato** (incremento 8): campo `*libav.AVPacket` (C `mb_pkt` embedded — mb possiede un ref). `MediaBufFromAVPkt`: alloc via `AvPacketAlloc`, ref via `AvPacketRefPtr(p.CPtr(), src.CPtr())` (src normalizzato a `*AVPacket` dal type-switch, `unsafe.Pointer` → `WrapAVPacket` borrow); dtor `mediaBufDtorAVPacket` = `AvPacketUnref` + `AvPacketFree` + nil. Call site liberi da `WrapAVPacket`.
- **Residui finali tipizzati** (incremento 12): `AVFormatCtx.ctx` → `*libav.AVFormatContext` interno (76 siti: `.CPtr()` dentro le call C; `Close()` estrae `cCtx` locale per `ml_avformat_close_input(&cCtx)` + nil; `LibavCtx()` = return diretto); `MediaCodecCreateLavcWithFormat` param `fmtCtx *libav.AVFormatContext` (sempre nil ai call site — seam compat C `avformat_find_stream_info` path); `VideoSubtitlesLavc` param `ctx *AVCodecContext` (call site `mc.Ctx` diretto); `AudioDecoder.ctx`/`frame` → `*AVCodecContext`/`*AVFrame` (`AvFrameFree` tipizzato, `ad.ctx` mai letto — rimosso in incremento 13). **Classificazione finale residui `unsafe.Pointer`**: tutti genuini — data/byte pointers C (`&mb.Data[0]`, extradata, samples), opaque callback handles (avio `opaque`, JNI/jobject), hwaccel device refs (`AVBufferRef*` devref, VdpDevice/GetProcAddress, D3D handles), `gvsRefAux` renderer seam, e `unsafe.Pointer(cctxPtr)` sul tipo `*C.AVCodecContext` corretto dentro gli shim `cgo.go`.
- **Dead code eliminato** (incremento 13): `AVFormatCtx.CopyCodecParams`, `AVFormatCtx.GetRawContext`, `medialibav.AvcodecFreeContext(*unsafe.Pointer)` + shim `ml_avcodec_free_context` in media/libav, `CtxIsVdpau` + helper `ml_vdpau_ctx_is_vdpau`, `AudioDecoder.ctx` (mai letto), `libav.AvcodecFreeContextRaw` (zero chiamanti; commento in `media/core/types.go` aggiornato a `AvcodecFreeContext`). Rimozione dopo audit: nessun chiamante vivo in build, vet, test -race verdi.
