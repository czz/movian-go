// Canonical 1:1 port of src/ecmascript/es_fs.c — file descriptor resources,
// open/read/write/fsize/ftruncate/rename/mkdirs/dirname/basename/copy.
package ecmascript

import (
	"io"
	"strings"

	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/trace"
)

// ---------------------------------------------------------------------------
// es_fd_t — C: es_fs.c:30-34
// ---------------------------------------------------------------------------

// C: es_fd_t
type esFD struct {
	super   *ESResource
	efdPath string
	efdFH   *facore.Handle
}

// esFDDestroy — C: es_fd_destroy (es_fs.c:44-52)
func esFDDestroy(eres *ESResource) {
	efd := eres.Data.(*esFD)
	if efd.efdFH != nil {
		facore.Close(efd.efdFH)
		efd.efdFH = nil
	}
	EsResourceUnlink(efd.super)
}

// esFDInfo — C: es_fd_info (es_fs.c:59-63)
func esFDInfo(eres *ESResource) string {
	efd := eres.Data.(*esFD)
	return efd.efdPath
}

// C: es_resource_fd (es_fs.c:68-73)
var esResourceFD = &ESResourceClass{
	ErcName:    "filedescriptor",
	ErcSize:    0, // sizeof(es_fd_t) — Go allocates by type
	ErcDestroy: esFDDestroy,
	ErcInfo:    esFDInfo,
}

// getFilename — C: get_filename (es_fs.c:81-107)
func getFilename(ctx *gaftape.Context, index int, ec *ESContext,
	forWrite int) string {
	filename := ctx.ToString(index)

	if esGconf().BypassEcmascriptACL {
		return filename
	}

	if forWrite != 0 && ec.ecBypassFileACLWrite {
		return filename
	}

	if forWrite == 0 && ec.ecBypassFileACLRead {
		return filename
	}

	if strings.Contains(filename, "../") || strings.Contains(filename, "/..") {
		ctx.Error(gaftape.GAF_ERR_ERROR,
			"Bad filename %s -- Contains parent references", filename)
	}

	if (ec.ecStorage != "" && strings.HasPrefix(filename, ec.ecStorage)) ||
		(ec.ecPath != "" && strings.HasPrefix(filename, ec.ecPath)) {
		return filename
	}

	ctx.Error(gaftape.GAF_ERR_ERROR, "Bad filename %s -- Access not allowed",
		filename)
	return ""
}

// esFileOpen — C: es_file_open (es_fs.c:112-144)
func esFileOpen(ctx *gaftape.Context) int {
	ec := EsGet(ctx)

	flagsstr := ctx.ToString(1)

	var flags int
	if flagsstr == "r" {
		flags = 0
	} else if flagsstr == "w" {
		flags = facore.FaWrite
	} else if flagsstr == "a" {
		flags = facore.FaWrite | facore.FaAppend
	} else {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Invalid flags '%s' to open", flagsstr)
	}

	forWrite := 0
	if flags != 0 {
		forWrite = 1
	}
	filename := getFilename(ctx, 0, ec, forWrite)

	fh, err := facore.OpenEx(esEnv.fam,
		filename, nil, flags)
	if fh == nil || err != nil {
		errmsg := ""
		if err != nil {
			errmsg = err.Error()
		}
		ctx.Error(gaftape.GAF_ERR_ERROR, "Unable to open file '%s' -- %s",
			filename, errmsg)
	}

	efd := &esFD{}
	efd.super = EsResourceCreate(ec, esResourceFD, 0, efd).(*ESResource)
	efd.efdPath = filename
	efd.efdFH = fh

	EsResourcePush(ctx, efd.super)
	return 1
}

// esFDGet — C: es_fd_get (es_fs.c:153-162)
func esFDGet(ctx *gaftape.Context, idx int) *esFD {
	r := EsResourceGet(ctx, idx, esResourceFD)
	efd := r.(*ESResource).Data.(*esFD)
	if efd.efdFH == nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Filehandle for %s is closed",
			efd.efdPath)
	}
	return efd
}

// esFileRead — C: es_file_read (es_fs.c:168-195)
// fd, buffer, offset, length, position
func esFileRead(ctx *gaftape.Context) int {
	efd := esFDGet(ctx, 0)
	buf := ctx.RequireBufferData(1)
	bufsize := len(buf)

	offset := ctx.ToInt(2)
	length := ctx.ToInt(3)

	if offset+length > bufsize {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Buffer too small %d < %d + %d",
			bufsize, offset, length)
	}

	if !ctx.IsNull(4) {
		// Seek
		facore.Seek(efd.efdFH, int64(ctx.RequireNumber(4)), io.SeekStart)
	}

	r, _ := facore.Read(efd.efdFH, buf[offset:offset+length])
	if r < 0 {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Read error from '%s'", efd.efdPath)
	}

	ctx.PushInt(r)
	return 1
}

// esFileWrite — C: es_file_write (es_fs.c:199-231)
// fd, buffer, offset, length, position
func esFileWrite(ctx *gaftape.Context) int {
	efd := esFDGet(ctx, 0)
	buf := ctx.GetBuffer(1)
	bufsize := len(buf)
	var length int

	offset := ctx.ToInt(2)
	if ctx.IsNull(3) {
		length = bufsize
	} else {
		length = ctx.ToInt(3)
	}

	// Don't read past buffer end
	if offset+length > bufsize {
		length = bufsize - offset
	}

	if !ctx.IsNull(4) {
		// Seek
		facore.Seek(efd.efdFH, int64(ctx.RequireNumber(4)), io.SeekStart)
	}

	r, _ := facore.Write(efd.efdFH, buf[offset:offset+length])
	if r < 0 {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Write error to '%s'", efd.efdPath)
	}

	ctx.PushInt(r)
	return 1
}

// esFileFsize — C: es_file_fsize (es_fs.c:236-247)
func esFileFsize(ctx *gaftape.Context) int {
	efd := esFDGet(ctx, 0)

	siz, _ := facore.FSize(efd.efdFH)
	if siz < 0 {
		ctx.Error(gaftape.GAF_ERR_ERROR, "File not seekable")
	}
	ctx.PushNumber(float64(siz))
	return 1
}

// esFileFtruncate — C: es_file_ftruncate (es_fs.c:251-259)
func esFileFtruncate(ctx *gaftape.Context) int {
	efd := esFDGet(ctx, 0)
	facore.Ftruncate(efd.efdFH, int64(ctx.ToNumber(1)))
	return 0
}

// esFileRename — C: es_file_rename (es_fs.c:264-279)
func esFileRename(ctx *gaftape.Context) int {
	ec := EsGet(ctx)

	oldname := getFilename(ctx, 0, ec, 0)
	newname := getFilename(ctx, 1, ec, 1)

	if err := facore.Rename(esEnv.fam,
		oldname, newname); err != nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Unable to rename '%s' to '%s' -- %s",
			oldname, newname, err.Error())
	}

	return 0
}

// esFileMkdirs — C: es_file_mkdirs (es_fs.c:283-298)
func esFileMkdirs(ctx *gaftape.Context) int {
	ec := EsGet(ctx)

	filename := getFilename(ctx, 0, ec, 1)

	if err := facore.Makedirs(esEnv.fam,
		filename); err != nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Unable to mkdir '%s' -- %s",
			filename, err.Error())
	}

	return 0
}

// esFileDirname — C: es_file_dirname (es_fs.c:303-316)
func esFileDirname(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	filename := getFilename(ctx, 0, ec, 0)

	if x := strings.LastIndex(filename, "/"); x >= 0 {
		ctx.PushString(filename[:x])
	}
	return 1
}

// esFileBasename — C: es_file_basename (es_fs.c:321-331)
func esFileBasename(ctx *gaftape.Context) int {
	ec := EsGet(ctx)

	ctx.PushString(esEnv.fam.FAURLGetLastComponent(getFilename(ctx, 0, ec, 0)))
	return 1
}

// esFileCopy — C: es_file_copy (es_fs.c:335-359)
func esFileCopy(ctx *gaftape.Context) int {
	ec := EsGet(ctx)

	from := ctx.ToString(0)
	to := ctx.ToString(1)

	cleanup := facore.SanitizeFilename(to)

	path := ec.ecStorage + "/copy/" + cleanup

	esEnv.tracer.Trace(trace.TRACE_DEBUG, "JS",
		"Copying file from '%s' to '%s'", from, path)

	if err := facore.Copy(esEnv.fam,
		path, from); err != nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Copy failed: %s", err.Error())
	}

	ctx.PushString(path)
	return 1
}

// ---------------------------------------------------------------------------
// fnlist_fs — C: es_fs.c:362-374
// ---------------------------------------------------------------------------

var esFnlistFS = []gaftape.FunctionListEntry{
	{Key: "open", Value: esFileOpen, Nargs: 3},
	{Key: "read", Value: esFileRead, Nargs: 5},
	{Key: "write", Value: esFileWrite, Nargs: 5},
	{Key: "fsize", Value: esFileFsize, Nargs: 1},
	{Key: "ftrunctae", Value: esFileFtruncate, Nargs: 2},
	{Key: "rename", Value: esFileRename, Nargs: 2},
	{Key: "mkdirs", Value: esFileMkdirs, Nargs: 2},
	{Key: "dirname", Value: esFileDirname, Nargs: 1},
	{Key: "basename", Value: esFileBasename, Nargs: 1},
	{Key: "copyfile", Value: esFileCopy, Nargs: 2},
}

// C: ES_MODULE("fs", fnlist_fs)
func registerEsFs() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "fs",
		Functions: esFnlistFS,
	})
}
