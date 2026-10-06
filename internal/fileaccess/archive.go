package fileaccess

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"sync"
)

// NewZIPProtocol creates a new ZIP protocol instance.
// The canonical implementation lives in fa_zip.go (fa_zip.c port).
func NewZIPProtocol(fam *FileAccessManager) *ZIPProtocol {
	return &ZIPProtocol{fam: fam}
}

// DataProtocol implements the data URI scheme (RFC 2397)
// Follows Go idiomatic principles:
// - Uses context.Context for cancellation
// - Encapsulates state in struct (no global state)
type DataProtocol struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.RWMutex
}

func (p *DataProtocol) Name() string {
	return "data"
}

func (p *DataProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, "data:")
}

func (p *DataProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	p.cancel = cancel

	// Parse data URI: data:[<mediatype>][;base64],<data>
	if !strings.HasPrefix(url, "data:") {
		return nil, errors.New("invalid data URI")
	}

	uri := url[5:] // Remove "data:"
	// C: data_open — strchr(uri, ',') → "No comma separator before payload"
	before, after, ok := strings.Cut(uri, ",")
	if !ok {
		return nil, errors.New("No comma separator before payload")
	}

	// C: find_str(uri, hdrlen, ";base64") → non-base64 data URIs rejected
	if !strings.Contains(before, ";base64") {
		return nil, errors.New("Data not base64 encoded")
	}
	payload := after

	// C: av_base64_decode — lenient (skips chars outside the alphabet)
	decoded, err := avBase64Decode(payload)
	if err != nil {
		cancel()
		return nil, errors.New("Invalid base64")
	}

	// C: data_read/data_seek share dh->fpos — one reader for both roles
	dr := &dataReader{reader: bytes.NewReader(decoded)}
	return &Handle{
		proto:    p,
		reader:   dr,
		seeker:   dr.reader.(io.Seeker),
		url:      url,
		size:     int64(len(decoded)),
		position: 0,
		closed:   false,
	}, nil
}

// avBase64Decode — C: av_base64_decode (libavutil/base64.c). FFmpeg
// ignores characters outside the base64 alphabet (incl. whitespace);
// StdEncoding rejects them, so filter first.
func avBase64Decode(s string) ([]byte, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' ||
			c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' {
			b.WriteByte(c)
		}
	}
	filtered := b.String()
	out, err := base64.StdEncoding.DecodeString(filtered)
	if err != nil {
		// unpadded fallback — av_base64_decode tolerates missing padding
		out, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(filtered, "="))
	}
	return out, err
}

func (p *DataProtocol) Stat(url string) (*FileStat, error) {
	// C: fa_protocol_data has no fap_stat → FAP_NOT_SUPPORTED
	return nil, ErrNotSupported
}

func (p *DataProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	// C: fa_protocol_data has no fap_scandir → FAP_NOT_SUPPORTED
	return nil, ErrNotSupported
}

func (p *DataProtocol) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cancel != nil {
		p.cancel()
	}

	return nil
}

// dataReader adapts data URI reader to io.ReadCloser
type dataReader struct {
	reader io.Reader
}

func (dr *dataReader) Read(p []byte) (int, error) {
	return dr.reader.Read(p)
}

func (dr *dataReader) Close() error {
	return nil
}

// torrentfile impl — delegation seam. The canonical fa_torrent.c port
// lives in pkg/backend/bittorrent (it needs bittorrent-internal state);
// facore can't import that package (import cycle), so the real
// implementation registers itself onto the manager's torrentfile proto
// at backend-registration time.

// TorrentFileProtocol implements the torrent file protocol
// Follows Go idiomatic principles:
// - Uses context.Context for cancellation
// - Encapsulates state in struct (no global state)
type TorrentFileProtocol struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.RWMutex
	impl   Protocol // bittorrent backend impl (C: link-time seam)
}

// NewTorrentFileProtocol creates a new torrent file protocol instance
// SetImpl injects the bittorrent-backed torrentfile implementation
// (C: fa_protocol_torrent registered by fa_torrent init).
func (p *TorrentFileProtocol) SetImpl(impl Protocol) { p.impl = impl }

func NewTorrentFileProtocol() *TorrentFileProtocol {
	ctx, cancel := context.WithCancel(context.Background())
	return &TorrentFileProtocol{
		ctx:    ctx,
		cancel: cancel,
	}
}

// C: fa_protocol_torrent (fa_torrent.c:392) — fap_name = "torrentfile".
func (p *TorrentFileProtocol) Name() string {
	return "torrentfile"
}

func (p *TorrentFileProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, "torrentfile://")
}

func (p *TorrentFileProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	if p.impl != nil {
		return p.impl.Open(url, extra)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	return nil, errors.New("torrent protocol not available - bittorrent backend not registered")
}

func (p *TorrentFileProtocol) Stat(url string) (*FileStat, error) {
	if p.impl != nil {
		return p.impl.Stat(url)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	return nil, errors.New("torrent protocol not available - bittorrent backend not registered")
}

func (p *TorrentFileProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	if p.impl != nil {
		return p.impl.ScanDir(ctx, url)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	return nil, errors.New("torrent protocol not available - bittorrent backend not registered")
}

func (p *TorrentFileProtocol) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cancel != nil {
		p.cancel()
	}

	return nil
}
