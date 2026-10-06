package bittorrent

// Canonical port of src/backend/bittorrent/fa_torrent.c.
//
// Adaptation notes: C's fa_protocol_t is a function vtable; Go's fileaccess
// layer splits it into a Protocol interface (Open/Stat/ScanDir/CanHandle/Name,
// dispatched by the fam) plus the FAProtocol vtable (Deadline, Reference,
// Unreference, Title — resolved by name via lookupGlobalFAProtocol).
// The fam-visible "torrentfile" Protocol lives in facore (scaffold that
// delegates to the fam-injected impl); the canonical implementation
// here registers itself into that seam at backend registration.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/czz/movian-go/internal/arch"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
)

// torrentFileRoot — C: TORRENT_FILE_ROOT ((void *)-1) sentinel
var torrentFileRoot = &TorrentFile{}

// torrentResolveFile — C: torrent_resolve_file (fa_torrent.c:34-50)
func (btg *BtGlobal) torrentResolveFile(url string) (*TorrentFile, error) {
	to, err := btg.torrentOpenURL(&url)
	if to == nil {
		return nil, err
	}

	if url == "" {
		return torrentFileRoot, nil
	}

	for tf := to.files.tqFirst; tf != nil; tf = tf.torrentLinkNext {
		if url == tf.fullpath {
			return tf, nil
		}
	}
	return nil, errors.New("File not found")
}

// torrentScandir — C: torrent_scandir (fa_torrent.c:57-98)
func (btg *BtGlobal) torrentScandir(fap *facore.FAProtocol, fd *facore.Dir, url string,
	flags int) error {
	to, err := btg.torrentOpenURL(&url)
	if to == nil {
		btg.mu.Unlock()
		return err
	}

	var tfq *torrentFileQueue

	if url == "" {
		tfq = &to.root
	} else {
		var tf *TorrentFile
		for e := to.files.tqFirst; e != nil; e = e.torrentLinkNext {
			if url == e.fullpath {
				tf = e
				break
			}
		}

		if tf == nil || tf.size != 0 {
			btg.mu.Unlock()
			return errors.New("Not such directory")
		}
		tfq = &tf.files
	}

	var hashstr [41]byte
	misc.Bin2hex(hashstr[:], len(hashstr), to.infoHash[:], 20)

	for tf := tfq.tqFirst; tf != nil; tf = tf.parentLinkNext {
		buf := fmt.Sprintf("torrentfile://%s/%s",
			string(hashstr[:40]), tf.fullpath)
		typ := facore.ContentDir
		if tf.size != 0 {
			typ = facore.ContentFile
		}
		facore.DirAdd(fd, buf, tf.name, typ)
	}
	btg.mu.Unlock()
	return nil
}

// mkinfo — C: mkinfo (fa_torrent.c:105-116)
func (btg *BtGlobal) mkinfo(p, title *propcore.Prop) *propcore.Prop {
	node := btg.propManager.RefInc(btg.propManager.CreateEx(p, "", nil, false, false))

	dstTitle := btg.propManager.RefInc(btg.propManager.CreateEx(node, "title", nil, false, false))
	btg.propManager.Link(title, dstTitle, nil, false, false)
	btg.propManager.RefDec(dstTitle)

	info := btg.propManager.RefInc(btg.propManager.CreateEx(node, "info", nil, false, false))
	btg.propManager.RefDec(node)
	return info
}

// torrentCancel — C: torrent_cancel (fa_torrent.c:123-130)
func (btg *BtGlobal) torrentCancel(opaque any) {
	tfh := opaque.(*TorrentFh)
	btg.mu.Lock()
	defer btg.mu.Unlock()
	tfh.cancelled = true
	btg.pieceVerified.Broadcast()
}

// torrentOpen — C: torrent_open (fa_torrent.c:136-175)
func (btg *BtGlobal) torrentOpen(proto facore.Protocol, url string,
	flags int, foe *facore.OpenExtra) (*facore.Handle, error) {
	tf, rerr := btg.torrentResolveFile(url)
	if tf == nil || tf == torrentFileRoot {
		btg.mu.Unlock()
		// C returns NULL with an empty errbuf for the root sentinel —
		// the fam needs a non-nil error for a failed open.
		if rerr == nil {
			rerr = errors.New("Unable to open torrent file")
		}
		return nil, rerr
	}

	tfh := &TorrentFh{btg: btg}

	if foe != nil {
		if stats, ok := foe.Stats.(*propcore.Prop); ok && stats != nil {
			tfh.faStats = btg.propManager.RefInc(stats)

			tfh.faStats.CreateInt("bitrateValid", 1)

			info := btg.propManager.RefInc(btg.propManager.CreateEx(tfh.faStats, "infoNodes",
				nil, false, false))

			tfh.torrentSeeders = btg.mkinfo(info, nls.GetProp("Torrent seeders"))
			tfh.torrentLeechers = btg.mkinfo(info, nls.GetProp("Torrent leechers"))
			tfh.knownPeers = btg.mkinfo(info, nls.GetProp("Known peers"))
			tfh.connectedPeers = btg.mkinfo(info, nls.GetProp("Connected peers"))
			tfh.recvPeers = btg.mkinfo(info, nls.GetProp("Receiving from"))

			btg.propManager.RefDec(info)
		}
	}
	tfh.file = tf
	to := tf.torrent
	listInsertHead(&tf.fhs.lhFirst, tfh, tfhTorrentFileLink)
	listInsertHead(&to.fhs.lhFirst, tfh, tfhTorrentLink)
	btg.torrentRetain(to)
	btg.mu.Unlock()

	if foe != nil {
		if c, ok := foe.Cancellable.(*misc.Cancellable); ok && c != nil {
			tfh.cancellable = misc.CancellableBind(c, btg.torrentCancel, tfh)
		}
	}

	return facore.NewHandle(btg.fam, proto, url, tfh, nil, tfh,
		func() int64 { return int64(tfh.file.size) }), nil
}

// Read — C: torrent_read (fa_torrent.c:182-211), adapted to io.Reader.
func (tfh *TorrentFh) Read(buf []byte) (int, error) {
	tfh.btg.mu.Lock()

	tf := tfh.file
	fsize := tf.size

	if tfh.fpos >= fsize {
		tfh.btg.mu.Unlock()
		return 0, io.EOF
	}

	size := len(buf)
	if tfh.fpos+uint64(size) > fsize {
		size = int(fsize - tfh.fpos)
	}

	if size == 0 {
		tfh.btg.mu.Unlock()
		return 0, io.EOF
	}

	r := tfh.btg.torrentLoad(tf.torrent, buf[:size],
		tf.offset+tfh.fpos, size, tfh)

	tfh.btg.mu.Unlock()
	tfh.fpos += uint64(r)
	return r, nil
}

// Seek — C: torrent_seek (fa_torrent.c:218-246), adapted to io.Seeker.
func (tfh *TorrentFh) Seek(pos int64, whence int) (int64, error) {
	var np int64

	switch whence {
	case io.SeekStart:
		np = pos

	case io.SeekCurrent:
		np = int64(tfh.fpos) + pos

	case io.SeekEnd:
		np = int64(tfh.file.size) + pos

	default:
		return -1, fmt.Errorf("invalid whence")
	}

	if np < 0 {
		return -1, fmt.Errorf("negative position")
	}

	tfh.fpos = uint64(np)
	return np, nil
}

// Close — C: torrent_close (fa_torrent.c:252-276), adapted to io.Closer.
func (tfh *TorrentFh) Close() error {
	misc.CancellableUnbind(tfh.cancellable, tfh)

	tfh.btg.mu.Lock()

	listRemove(tfh, tfhTorrentFileLink)
	listRemove(tfh, tfhTorrentLink)

	tfh.btg.torrentRelease(tfh.file.torrent)

	tfh.btg.mu.Unlock()

	if tfh.faStats != nil {
		tfh.btg.propManager.RefDec(tfh.faStats)
	}
	if tfh.torrentSeeders != nil {
		tfh.btg.propManager.RefDec(tfh.torrentSeeders)
	}
	if tfh.torrentLeechers != nil {
		tfh.btg.propManager.RefDec(tfh.torrentLeechers)
	}
	if tfh.knownPeers != nil {
		tfh.btg.propManager.RefDec(tfh.knownPeers)
	}
	if tfh.connectedPeers != nil {
		tfh.btg.propManager.RefDec(tfh.connectedPeers)
	}
	if tfh.recvPeers != nil {
		tfh.btg.propManager.RefDec(tfh.recvPeers)
	}
	// C: free(tfh) — GC
	return nil
}

// torrentStat — C: torrent_stat (fa_torrent.c:294-314)
func (btg *BtGlobal) torrentStat(fap *facore.FAProtocol, url string,
	flags int) (*facore.FileStat, error) {
	tf, err := btg.torrentResolveFile(url)
	if tf == nil {
		btg.mu.Unlock()
		return nil, err
	}

	fs := &facore.FileStat{}

	if tf == torrentFileRoot {
		fs.Type = facore.ContentDir
	} else {
		fs.Size = int64(tf.size)
		if tf.size != 0 {
			fs.Type = facore.ContentFile
		} else {
			fs.Type = facore.ContentDir
		}
	}

	btg.mu.Unlock()
	return fs, nil
}

// torrentDeadline — C: torrent_deadline (fa_torrent.c:321-325)
func (btg *BtGlobal) torrentDeadline(fh *facore.Handle, deadline int) {
	if tfh, ok := fh.Reader().(*TorrentFh); ok {
		tfh.deadline = arch.GetTS() + int64(deadline)
	}
}

// C: typedef struct torrent_fh_ref { fa_handle_t h; torrent_t *to; }
// (fa_torrent.c:331-334)
type torrentFhRef struct {
	btg *BtGlobal
	to  *Torrent
}

// Read — placeholder so torrentFhRef satisfies io.ReadCloser.
func (tfr *torrentFhRef) Read(buf []byte) (int, error) { return 0, io.EOF }

// Close — placeholder; the real teardown is fap_unreference.
func (tfr *torrentFhRef) Close() error { return nil }

// torrentReference — C: torrent_reference (fa_torrent.c:341-355)
func (btg *BtGlobal) torrentReference(fap *facore.FAProtocol, url string) *facore.Handle {
	to, _ := btg.torrentOpenURL(&url)
	if to == nil {
		btg.mu.Unlock()
		return nil
	}

	tfr := &torrentFhRef{btg: btg, to: to}
	btg.torrentRetain(to)
	btg.mu.Unlock()
	return facore.NewHandle(btg.fam, torrentFileProtocol{btg: btg}, url, tfr, nil, nil, nil)
}

// torrentUnreference — C: torrent_unreference (fa_torrent.c:362-369)
func (btg *BtGlobal) torrentUnreference(fh *facore.Handle) {
	tfr, ok := fh.Reader().(*torrentFhRef)
	if !ok {
		return
	}
	btg.mu.Lock()
	defer btg.mu.Unlock()
	btg.torrentRelease(tfr.to)
	// C: free(fh) — GC
}

// torrentTitle — C: torrent_title (fa_torrent.c:376-386)
func (btg *BtGlobal) torrentTitle(fap *facore.FAProtocol, url string) string {
	var r string
	to, _ := btg.torrentOpenURL(&url)

	if to != nil && url == "" {
		r = to.title
	}

	btg.mu.Unlock()
	return r
}

// torrentFileProtocol — Go Protocol-interface adapter for the canonical
// fa_protocol_torrent vtable. Registered onto fam.torrentfileProto
// at backend registration.
type torrentFileProtocol struct{ btg *BtGlobal }

// Name — C: fap_name = "torrentfile" (fa_torrent.c:393)
func (fp torrentFileProtocol) Name() string { return "torrentfile" }

// CanHandle — matches the fam dispatch for "torrentfile://" URLs.
func (fp torrentFileProtocol) CanHandle(url string) bool {
	return len(url) >= 13 && url[:13] == "torrentfile://"
}

// Open — C: torrent_open (fap_open); errbuf text folds into the error.
func (fp torrentFileProtocol) Open(url string,
	extra *facore.OpenExtra) (*facore.Handle, error) {
	flags := 0
	if extra != nil {
		flags = extra.Flags
	}
	// C: fa_resolve_proto hands fap_open the fname with "proto://"
	// stripped (torrentfile carries no FAP_INCLUDE_PROTO_IN_URL) —
	// the fam-level dispatch passes the full URL, so drop the scheme.
	fname := strings.TrimPrefix(url, "torrentfile://")
	return fp.btg.torrentOpen(fp, fname, flags, extra)
}

// Stat — C: torrent_stat (fap_stat).
func (fp torrentFileProtocol) Stat(url string) (*facore.FileStat, error) {
	// C: fa_resolve_proto strips "torrentfile://" before fap_stat.
	fname := strings.TrimPrefix(url, "torrentfile://")
	return fp.btg.torrentStat(nil, fname, 0)
}

// ScanDir — C: torrent_scandir (fap_scan).
func (fp torrentFileProtocol) ScanDir(ctx context.Context,
	url string) (*facore.Dir, error) {
	fd := &facore.Dir{Entries: make(map[string]*facore.DirEntry)}
	// C: fa_resolve_proto strips "torrentfile://" before fap_scan.
	fname := strings.TrimPrefix(url, "torrentfile://")
	if err := fp.btg.torrentScandir(nil, fd, fname, 0); err != nil {
		return nil, err
	}
	return fd, nil
}

// faTorrentRegister — C: FAP_REGISTER(torrent). Registers the fap
// vtable (fa_protocol_torrent, fa_torrent.c:392-405) and installs the
// canonical Protocol impl into the fam delegation seam. The
// Scan/Open/Read/Seek/Close/Fsize slots are covered by the Protocol
// impl above; the FAProtocol registers the remaining fap-level hooks.
func (btg *BtGlobal) faTorrentRegister() {
	btg.fam.RegisterFAProtocol(&facore.FAProtocol{
		Name:        "torrentfile",
		Deadline:    btg.torrentDeadline,
		Reference:   btg.torrentReference,
		Unreference: btg.torrentUnreference,
		Title:       btg.torrentTitle,
		Stat:        btg.torrentStat,
	})
	btg.fam.TorrentfileProto().SetImpl(torrentFileProtocol{btg: btg})
}
