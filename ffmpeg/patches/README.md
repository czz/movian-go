# FFmpeg vendored patches

L'albero `ffmpeg/ffmpeg-*/` è estratto a build-time dal tarball verificato
(`scripts/deps.env` SHA-256) ed è gitignored: le modifiche al sorgente NON
persistono. Ogni deviazione da upstream vive qui come `.patch`, applicata
idempotentemente da `apply_ffmpeg_patches()` in `scripts/fetch_dep.sh`
(tutti gli script build_ffmpeg_* la invocano dopo l'estrazione).

Tema comune: FFmpeg è compilato **senza protocolli di rete nativi**
(niente openssl/gnutls) — tutto l'I/O media passa da fileaccess → Go
`net/http` + `crypto/tls` via `AVIOContext` custom e `io_open`. Le patch
insegnano ai punti che validavano lo schema contro la URLProtocol-list
statica a fidarsi del custom I/O quando `AVFMT_FLAG_CUSTOM_IO` è attivo.

## Patch

| File | Target | Cosa fa |
|---|---|---|
| `hls-custom-io-scheme.patch` | `libavformat/hls.c` | Se `avio_find_protocol_name` torna NULL e c'è custom I/O, deriva lo scheme dall'URL per le whitelist-check e prosegue via `io_open` (segmenti/playlist https serviti da Go TLS) |
| `dashdec-custom-io-scheme.patch` | `libavformat/dashdec.c` | idem per DASH |
| `avio-https-warning-on-open.patch` | `libavformat/avio.c` | Sposta il warning "https or dtls protocol not found, recompile with openssl…" da `url_find_protocol` (sparato a ogni probe, incluse le check benigne dei demuxer) al failure path reale di `ffurl_alloc` — l'hint resta solo quando un open nativo fallisce davvero |
| `seek-custom-io-proto.patch` | `libavformat/seek.c` | `ff_configure_buffers_for_index` decide local-vs-network dallo scheme; con custom I/O e scheme non registrato ricavava NULL → warning + buffer tuning "da file locale" su stream di rete. Ora deriva lo scheme dall'URL (bare path → `file`, come `url_find_protocol`): https prende il path network corretto |

Lato C del port: `fa_io_open_cb` in `internal/libav/libav_avio.c` logga
una volta (INFO) `custom io_open: nested opens via fileaccess
(external TLS)` — il log dice chi fa il TLS.

## Limitazioni note (upstream, documentate)

### "Late SEI is not implemented" (h264)

`[h264 @ …] Late SEI is not implemented. Update your FFmpeg version…`

- **Decisione**: nessuna azione — comportamento upstream deliberato.
- Il guard `if (h->setup_finished)` in `h264dec.c` fu aggiunto dal commit
  upstream `4895759` "avcodec/h264dec: Skip late SEI — Fixes: Race
  condition" (FFmpeg 5.0, trovato da ClusterFuzz): decodificare SEI dopo
  il setup del frame-threading raceava, perché `h->sei` è stato già
  copiato nei worker thread.
- **Anche FFmpeg git master ha lo stesso codice** — il messaggio
  standard di `avpriv_request_sample` è fuorviante: nessun aggiornamento
  risolve. Il fix "vero" richiederebbe un rework della propagazione dello
  stato SEI ai thread (upstream non l'ha fatto).
- **Impatto**: decode video inalterato; si perdono solo metadati
  supplementari mid-stream (film grain aggiornato, display orientation,
  HDR dinamico, recovery point, captions CEA-608/708 in SEI tardivi).
- Su stream che ripetono SEI a ogni IDR il warning spam una volta per
  GOP (`request_sample` non fa dedup). Se servisse, candidato unico:
  dedup del log — il drop resta.

## Convenzione

- Formato `git diff` con path `a/libavformat/…` `b/libavformat/…`
  (applicati con `patch -p1` dalla root del source tree).
- Test su tree vergine: `tar -xf ffmpeg/ffmpeg-9.0.1.tar.xz` in una dir
  temporanea + `patch -p1 --dry-run < …` per ogni file.
- Ogni patch deve essere idempotente/sicura su tree già patchato
  (apply_ffmpeg_patches fa `--dry-run` prima).
