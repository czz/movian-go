// Canonical 1:1 port of src/ecmascript/es_crypto.c
//
// Hash object: sha1/sha256/sha512/md5 via crypto/sha1|sha256|sha512|md5
// (C uses libavutil AVSHA/AVMD5 — streaming hash interface).
package ecmascript

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"hash"

	"github.com/czz/movian-go/internal/gaftape"
)

// C: typedef struct es_hash (es_crypto.c:27-38)
const (
	ES_HASH_SHA = 0
	ES_HASH_MD5 = 1
)

type esHash struct {
	mode      int
	digestLen int
	h         hash.Hash // C: union { AVSHA *sha; AVMD5 *md5 }
}

// es_hash_release — C: es_crypto.c:41-52
func esHashRelease(ptr any) {
	// C: av_freep + free — Go GC handles; hash state released.
	eh := ptr.(*esHash)
	eh.h = nil
}

// ES_NATIVE_CLASS(hash, &es_hash_release) — C: es_crypto.c:54
var esNativeHash = &EcmascriptNativeClass{
	Name:    "hash",
	Release: esHashRelease,
}

func registerEsCryptoa() {
	ecmascriptRegisterNativeClass(esNativeHash)
}

// ---------------------------------------------------------------------------
// es_hashCreate — C: es_crypto.c:60-88
// ---------------------------------------------------------------------------

func esHashCreate(ctx *gaftape.Context) int {
	algo := ctx.RequireString(0)
	eh := &esHash{}

	switch algo {
	case "sha1":
		eh.h = sha1.New()
		eh.mode = ES_HASH_SHA
		eh.digestLen = 20
	case "sha256":
		eh.h = sha256.New()
		eh.mode = ES_HASH_SHA
		eh.digestLen = 32
	case "sha512":
		eh.h = sha512.New()
		eh.mode = ES_HASH_SHA
		eh.digestLen = 64
	case "md5":
		eh.h = md5.New()
		eh.mode = ES_HASH_MD5
		eh.digestLen = 16
	default:
		ctx.Error(gaftape.GAF_ERR_ERROR, "Unknown hash algo %s", algo)
	}
	esPushNativeObj(ctx, esNativeHash, eh)
	return 1
}

// ---------------------------------------------------------------------------
// es_hashUpdate — C: es_crypto.c:93-116
// ---------------------------------------------------------------------------

func esHashUpdate(ctx *gaftape.Context) int {
	eh := esGetNativeObj(ctx, 0, esNativeHash).(*esHash)
	var buf []byte
	if ctx.IsBuffer(1) {
		buf = ctx.GetBuffer(1)
	} else {
		buf = []byte(ctx.ToString(1))
	}
	eh.h.Write(buf)
	return 0
}

// ---------------------------------------------------------------------------
// es_hashFinalize — C: es_crypto.c:121-137
// ---------------------------------------------------------------------------

func esHashFinalize(ctx *gaftape.Context) int {
	eh := esGetNativeObj(ctx, 0, esNativeHash).(*esHash)

	digest := ctx.PushBuffer(eh.digestLen, false)

	d := eh.h.Sum(nil)
	copy(digest, d)
	return 1
}

// ---------------------------------------------------------------------------
// fnlist + module — C: es_crypto.c:155-162
// ---------------------------------------------------------------------------

var fnlistCrypto = []gaftape.FunctionListEntry{
	{Key: "hashCreate", Value: esHashCreate, Nargs: 1},
	{Key: "hashUpdate", Value: esHashUpdate, Nargs: 2},
	{Key: "hashFinalize", Value: esHashFinalize, Nargs: 1},
}

// ES_MODULE("crypto", fnlist_crypto)
func registerEsCryptob() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "crypto",
		Functions: fnlistCrypto,
	})
}
