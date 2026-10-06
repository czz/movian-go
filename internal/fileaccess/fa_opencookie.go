package fileaccess

import "io"

// C: static ssize_t cookie_read(void *fh, char *buf, size_t size)
func cookieRead(fh *Handle, buf []byte) (int, error) {
	return FARead(fh, buf)
}

// C: static int cookie_seek(void *fh, off64_t *offsetp, int whence)
// C: *offsetp = fa_seek(fh, *offsetp, whence); return 0;
func cookieSeek(fh *Handle, offset int64, whence int) (int64, error) {
	s, err := FASeek(fh, offset, whence)
	if err != nil {
		return 0, err
	}
	return s, nil
}

// C: static int cookie_close(void *fh)
// C: fa_close(fh); return 0;
func cookieClose(fh *Handle) error {
	fh.Close()
	return nil
}

// faCookie wraps a *Handle as an io.ReadSeekCloser, matching C's
// fopencookie stream. When doclose is false, Close is a no-op
// (C: fn_noclose has no .close callback).
type faCookie struct {
	fh      *Handle
	doclose bool
}

func (c *faCookie) Read(p []byte) (int, error) {
	return cookieRead(c.fh, p)
}

func (c *faCookie) Seek(offset int64, whence int) (int64, error) {
	return cookieSeek(c.fh, offset, whence)
}

func (c *faCookie) Close() error {
	if !c.doclose {
		return nil
	}
	return cookieClose(c.fh)
}

// C: FILE *fa_fopen(fa_handle_t *fh, int doclose)
// C: fopencookie(fh, "rb", doclose ? fn_full : fn_noclose)
// Go: io.ReadSeekCloser is the equivalent stream abstraction.
func FAFopen(fh *Handle, doclose int) io.ReadSeekCloser {
	return &faCookie{fh: fh, doclose: doclose != 0}
}
