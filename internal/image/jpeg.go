package image

// Canonical port of src/image/jpeg.c + jpeg.h.

import (
	"errors"
	"fmt"
	"time"
)

// jpegreader_t — C: int (*)(void *handle, void *buf,
// int64_t offset, size_t size)
type JpegReaderFunc func(handle any, buf []byte, offset int64, size int) int

// JPEGInfo — C: jpeginfo_t (jpeg.h:26-37).
type JPEGInfo struct {
	Width        int    // C: ji_width
	Height       int    // C: ji_height
	Orientation  int    // C: ji_orientation (char)
	Progressive  int    // C: ji_progressive (char)
	Components   int    // C: ji_components (char)
	Thumbnail    *Image // C: ji_thumbnail
	Time         int64  // C: ji_time (time_t)
	Manufacturer string // C: ji_manufacturer (rstr)
	Equipment    string // C: ji_equipment (rstr)
}

// C: jpeg.h flags
const (
	JPEGInfoDimensions  = 0x1
	JPEGInfoThumbnail   = 0x2
	JPEGInfoOrientation = 0x4
	JPEGInfoMetadata    = 0x8
)

// jiparser_t — C: typedef int (jiparser_t)(jpeginfo_t *, const
// uint8_t *, size_t, int) (jpeg.c:36). buf carries the full loadbuf
// (ll = mlen+4 bytes — C parsers may read a few bytes past `length`
// into the prefetched next-marker slack, e.g. parse_sof's buf[5]
// when len==5); length is the C `len` argument.
type jiparserFunc func(ji *JPEGInfo, buf []byte, length int, flags int) int

// jpegpriv — C: jpegpriv_t (jpeg.c:42-50).
type jpegpriv struct {
	readbuf       []byte
	readbufOffset int64
	readbufEnd    int64
	readhandle    any
	reader        JpegReaderFunc
}

// parseSOF — C: parse_sof (jpeg.c:57-67).
func parseSOF(ji *JPEGInfo, buf []byte, length int, flags int) int {
	if length < 5 {
		return -1
	}

	ji.Height = int(buf[1])<<8 | int(buf[2])
	ji.Width = int(buf[3])<<8 | int(buf[4])
	if length >= 5 {
		ji.Components = int(buf[5])
	}
	return 0
}

var exifheader = [6]byte{0x45, 0x78, 0x69, 0x66, 0x00, 0x00}

// jpegTime — C: jpeg_time (jpeg.c:75-95).
// sscanf("%d%c%d%c%d %d:%d:%d") + timegm.
func jpegTime(d string) int64 {
	var year, mon, mday, hour, min, sec int
	var dummy1, dummy2 byte

	n, _ := fmt.Sscanf(d, "%d%c%d%c%d %d:%d:%d",
		&year, &dummy1, &mon, &dummy2, &mday,
		&hour, &min, &sec)
	if n != 8 {
		return 0
	}

	// C: tm_mon-- / tm_year -= 1900 / tm_isdst = -1 / timegm
	return time.Date(year, time.Month(mon), mday,
		hour, min, sec, 0, time.UTC).Unix()
}

// parseAPP1 — C: parse_app1 (jpeg.c:101-245).
func parseAPP1(ji *JPEGInfo, buf []byte, length int, flags int) int {
	var bigendian bool
	ifd := 0

	thumbnailJpegOffset := -1
	thumbnailJpegSize := -1

	exif8 := func(off int) uint8 { return buf[off] }
	exif16 := func(off int) uint16 {
		if bigendian {
			return uint16(buf[off])<<8 | uint16(buf[off+1])
		}
		return uint16(buf[off+1])<<8 | uint16(buf[off])
	}
	exif32 := func(off int) uint32 {
		if bigendian {
			return uint32(buf[off])<<24 | uint32(buf[off+1])<<16 |
				uint32(buf[off+2])<<8 | uint32(buf[off+3])
		}
		return uint32(buf[off+3])<<24 | uint32(buf[off+2])<<16 |
			uint32(buf[off+1])<<8 | uint32(buf[off])
	}
	ifdtag := func(ifd, tag int) int { return ifd<<16 | tag }

	// Exif Header
	if length < 6 ||
		string(buf[:6]) != string(exifheader[:]) {
		return 0 // Don't fail here, just skip
	}

	buf = buf[6:]
	length -= 6

	// TIFF header
	if length < 8 {
		return -1
	}

	if buf[0] == 'M' && buf[1] == 'M' {
		bigendian = true
	} else if buf[0] == 'I' && buf[1] == 'I' {
		bigendian = false
	} else {
		return -1
	}

	if exif16(2) != 0x2a {
		return -1
	}

	ifdbase := int(exif32(4))

	for ifdbase != 0 {

		if length < ifdbase+2 {
			return -1
		}

		entries := int(exif16(ifdbase))

		if length < ifdbase+2+entries*12+4 {
			return -1
		}

		for i := range entries {
			tag := int(exif16(ifdbase + 2 + i*12 + 0))
			typ := int(exif16(ifdbase + 2 + i*12 + 2))
			c := int(exif32(ifdbase + 2 + i*12 + 4))

			po := ifdbase + 2 + i*12 + 8
			value := 0
			str := ""
			strSet := false
			switch typ {
			case 1:
				value = int(int8(exif8(po)))
			case 2:
				if c > 0 {
					c--
					if c < 4 {
						str = string(buf[po : po+c])
						strSet = true
					} else {
						value = int(exif32(po))
						if value+c <= length {
							str = string(buf[value : value+c])
							strSet = true
						}
					}
				}
			case 3:
				value = int(uint16(exif16(po)))
			case 4:
				value = int(exif32(po))
			case 6:
				value = int(int8(exif8(po)))
			case 8:
				value = int(int16(exif16(po)))
			}

			switch ifdtag(ifd, tag) {
			case ifdtag(1, 0x201): // JPEG Thumbnail offset
				thumbnailJpegOffset = value
			case ifdtag(1, 0x202): // JPEG Thumbnail size
				thumbnailJpegSize = value
			case ifdtag(0, 0x112): // Orientation
				ji.Orientation = value
			case ifdtag(0, 0x132): // Datetime
				ji.Time = jpegTime(str)
			case ifdtag(0, 0x10f): // Manufacturer
				if strSet {
					ji.Manufacturer = str
				} else {
					ji.Manufacturer = ""
				}
			case ifdtag(0, 0x110): // Equipment
				if strSet {
					ji.Equipment = str
				} else {
					ji.Equipment = ""
				}
			}
		}

		ifd++
		ifdbase = int(exif32(ifdbase + 2 + entries*12))
	}

	if flags&JPEGInfoThumbnail != 0 &&
		thumbnailJpegOffset != -1 && thumbnailJpegSize != -1 &&
		thumbnailJpegOffset+thumbnailJpegSize <= length {

		ji.Thumbnail = CodedCreateFromData(
			buf[thumbnailJpegOffset:thumbnailJpegOffset+thumbnailJpegSize],
			CodedJPEG)
		ji.Thumbnail.Flags |= FlagThumbnail
		ji.Thumbnail.Orientation = uint8(ji.Orientation)
	}
	return 0
}

// jpegRead — C: jpeg_read (jpeg.c:252-272). Buffered reader.
func jpegRead(jp *jpegpriv, buf []byte, offset int64, size int) int {
	if jp.readbuf != nil && offset >= jp.readbufOffset &&
		offset+int64(size) <= jp.readbufEnd {
		copy(buf[:size],
			jp.readbuf[offset-jp.readbufOffset:])
	} else {
		r := size + 1024
		if cap(jp.readbuf) < r {
			jp.readbuf = make([]byte, r)
		} else {
			jp.readbuf = jp.readbuf[:r]
		}
		r = jp.reader(jp.readhandle, jp.readbuf, offset, r)

		if r < size {
			return -1
		}
		copy(buf[:size], jp.readbuf[:size])
		jp.readbufOffset = offset
		jp.readbufEnd = offset + int64(r)
	}
	return size
}

// JpegInfo — C: jpeg_info (jpeg.c:279-382). The diagnostic text that C
// writes into errbuf is returned as the error (last write wins).
func JpegInfo(ji *JPEGInfo, reader JpegReaderFunc, handle any,
	flags int, buf []byte) (int, error) {
	var loadbuf []byte
	offset := 0
	var jip jiparserFunc
	var err error

	*ji = JPEGInfo{}

	ji.Width = -1
	ji.Height = -1

	jp := &jpegpriv{
		readhandle: handle,
		reader:     reader,
	}

	if len(buf) < 2 || buf[0] != 0xff || buf[1] != 0xd8 {
		return -1, errors.New("Invalid JPEG header")
	}

	buf = buf[2:]
	offset += 2

	for len(buf) >= 4 {
		marker := uint16(buf[0])<<8 | uint16(buf[1])
		mlen := int(buf[2])<<8 | int(buf[3])
		buf = buf[4:]
		offset += 4
		mlen -= 2

		jip = nil

		switch marker {
		case 0xffda: // SOS
			return 0, nil

		case 0xffc2, 0xffc6, 0xffca, 0xffce:
			// SOF2, SOF6, SOF10, SOF14
			ji.Progressive = 1

			fallthrough

		case 0xffc0, 0xffc1, 0xffc3: // SOF0, SOF1, SOF3
			if flags&JPEGInfoDimensions != 0 {
				jip = parseSOF
			}

		case 0xffe1: // APP1
			if flags&(JPEGInfoThumbnail|JPEGInfoOrientation|
				JPEGInfoMetadata) != 0 {
				jip = parseAPP1
			}
		}

		if jip != nil {
			ll := mlen + 4
			loadbuf = make([]byte, ll)

			if jpegRead(jp, loadbuf, int64(offset), ll) != ll {
				err = errors.New("Read error")
				break
			}
			buf = loadbuf

			if jip(ji, loadbuf, ll-4, flags) != 0 {
				err = fmt.Errorf(
					"Error while  processing section 0x%04x",
					marker)
				break
			}
			// Continue with bytes after section
			buf = loadbuf[mlen:]
			offset += mlen

		} else {

			loadbuf = make([]byte, 4)
			offset += mlen
			if jpegRead(jp, loadbuf, int64(offset), 4) != 4 {
				err = errors.New("Read error")
				break
			}
			buf = loadbuf
		}
	}

	return -1, err
}

// JpegInfoClear — C: jpeg_info_clear (jpeg.c:389-395).
func JpegInfoClear(ji *JPEGInfo) {
	if ji.Thumbnail != nil {
		ji.Thumbnail.Release()
	}
	ji.Manufacturer = ""
	ji.Equipment = ""
}

// JpegMeminfo — C: jpeg_meminfo_t (jpeg.h:56-59).
type JpegMeminfo struct {
	Data []byte
}

// JpeginfoMemReader — C: jpeginfo_mem_reader (jpeg.c:404-413).
func JpeginfoMemReader(handle any, buf []byte, offset int64,
	size int) int {
	mi := handle.(*JpegMeminfo)

	if int64(size)+offset > int64(len(mi.Data)) {
		size = len(mi.Data) - int(offset)
	}
	if size <= 0 {
		return size
	}

	copy(buf[:size], mi.Data[offset:])
	return size
}
