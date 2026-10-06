package misc

// Port of src/misc/bitstream.c + bitstream.h
//
// C: typedef struct bitstream {
//      union { const uint8_t *rdata; void *opaque; };
//      void (*skip_bits)(struct bitstream *bs, int num);
//      unsigned int (*read_bits)(struct bitstream *bs, int num);
//      unsigned int (*read_bits1)(struct bitstream *bs);
//      unsigned int (*read_golomb_ue)(struct bitstream *bs);
//      signed int (*read_golomb_se)(struct bitstream *bs);
//      int (*bits_left)(struct bitstream *bs);
//      int bytes_length, bytes_offset, remain;
//      uint8_t tmp, rbsp;
//    } bitstream_t;

type BitstreamT struct {
	Rdata []byte // C: union { const uint8_t *rdata; void *opaque; }

	SkipBits      func(bs *BitstreamT, num int) // C: function pointer
	ReadBits      func(bs *BitstreamT, num int) uint
	ReadBits1     func(bs *BitstreamT) uint
	ReadGolombUe  func(bs *BitstreamT) uint
	ReadGolombSe  func(bs *BitstreamT) int
	BitsLeftField func(bs *BitstreamT) int

	BytesLength int   // C: bytes_length
	BytesOffset int   // C: bytes_offset
	Remain      int   // C: remain
	Tmp         uint8 // C: tmp
	Rbsp        uint8 // C: rbsp
}

// C: static int bs_eof(const bitstream_t *bs)
func bsEof(bs *BitstreamT) int {
	if bs.BytesOffset >= bs.BytesLength {
		return 1
	}
	return 0
}

// C: static unsigned int read_bits(bitstream_t *bs, int num)
func readBits(bs *BitstreamT, num int) uint {
	r := uint(0)

	for num > 0 {
		if bs.BytesOffset >= bs.BytesLength {
			return 0
		}

		if bs.Remain == 0 {
			bs.Tmp = bs.Rdata[bs.BytesOffset]
			bs.BytesOffset++

			if bs.Rbsp != 0 && bs.BytesOffset >= 2 &&
				bs.BytesOffset < bs.BytesLength {
				if bs.Rdata[bs.BytesOffset-2] == 0 &&
					bs.Rdata[bs.BytesOffset-1] == 0 &&
					bs.Rdata[bs.BytesOffset] == 3 {
					bs.BytesOffset++
				}
			}
			bs.Remain = 8
		}

		num--
		bs.Remain--
		if bs.Tmp&(1<<bs.Remain) != 0 {
			r |= 1 << num
		}
	}
	return r
}

// C: static unsigned int read_bits1(bitstream_t *bs)
func readBits1(bs *BitstreamT) uint {
	return readBits(bs, 1)
}

// C: static void skip_bits(bitstream_t *bs, int num)
func skipBits(bs *BitstreamT, num int) {
	readBits(bs, num)
}

// C: static unsigned int read_golomb_ue(bitstream_t *bs)
func readGolombUe(bs *BitstreamT) uint {
	b := uint(0)
	lzb := -1
	for b == 0 && bsEof(bs) == 0 {
		b = readBits1(bs)
		lzb++
	}

	// C: (1 << lzb) - 1 + read_bits(bs, lzb). On all-zero/garbage tails
	// lzb can be -1 (no 1-bit before EOF) or ≥32; C's `1 << n` on int is
	// UB but the compiled x86 shifts mask the count to 5 bits, and
	// read_bits(bs, n<=0) returns 0 via its `num > 0` guard — the &31
	// mirrors the machine semantics of the C binary.
	return (1 << (uint(lzb) & 31)) - 1 + readBits(bs, lzb)
}

// C: static signed int read_golomb_se(bitstream_t *bs)
func readGolombSe(bs *BitstreamT) int {
	v := int(readGolombUe(bs))
	if v == 0 {
		return 0
	}

	pos := v & 1
	v = (v + 1) >> 1
	if pos != 0 {
		return v
	}
	return -v
}

// C: static int bits_left(struct bitstream *bs)
func bitsLeft(bs *BitstreamT) int {
	return bs.Remain + (bs.BytesLength-bs.BytesOffset)*8
}

// C: void init_rbits(bitstream_t *bs, const uint8_t *data, int length, int rbsp)
func SetupRbits(bs *BitstreamT, data []byte, length int, rbsp int) {
	bs.Rdata = data
	bs.BytesOffset = 0
	bs.BytesLength = length
	bs.Remain = 0
	bs.Rbsp = uint8(rbsp)

	bs.SkipBits = skipBits
	bs.ReadBits = readBits
	bs.ReadBits1 = readBits1
	bs.ReadGolombUe = readGolombUe
	bs.ReadGolombSe = readGolombSe
	bs.BitsLeftField = bitsLeft
}
