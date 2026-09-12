package rfb

import (
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
)

// maxCompressedBlock caps the length prefix of a compressed rectangle. The
// value is far above anything a real server sends (a 4K screen of raw 32bpp
// pixels is ~33MB and compresses down from there) and exists only so a
// corrupt or hostile length cannot turn into a huge allocation.
const maxCompressedBlock = 1 << 26 // 64 MiB

// blockSource feeds a zlib inflater from a sequence of discrete compressed
// blocks.
//
// This is the awkward part of RFB's compressed encodings: the zlib stream is
// per-connection and never terminates, but it arrives chopped into
// length-prefixed, per-rectangle blocks. The dictionary built up by earlier
// rectangles is what makes later ones small, so the inflater must survive
// across rectangles — creating a fresh zlib.Reader per rectangle would both
// throw away the dictionary and fail outright, because there is no zlib header
// at the start of the second block.
//
// Servers end each block with a Z_SYNC_FLUSH, which appends an empty stored
// block. That is what lets a bounded read work: flate only releases the
// pending output of a Huffman block once it has seen the block terminator, and
// the sync marker supplies it inside the same block we just pushed. So reading
// exactly the expected number of decompressed bytes never needs a byte that
// has not arrived yet.
//
// blockSource deliberately implements io.ByteReader as well as io.Reader:
// compress/flate wraps a plain io.Reader in its own bufio.Reader, which would
// strand already-consumed-from-the-wire bytes in a buffer we do not control.
// Satisfying flate.Reader keeps the inflater reading straight out of this
// struct, so all retained state lives here.
type blockSource struct {
	buf []byte
	off int
}

// push appends a compressed block. Any bytes of the previous block the
// inflater has not consumed yet are preserved ahead of the new ones.
func (s *blockSource) push(b []byte) {
	switch {
	case s.off >= len(s.buf):
		// Fully drained: reuse the buffer from the start.
		s.buf = s.buf[:0]
		s.off = 0
	case s.off > 0:
		// Partially drained: compact so the buffer cannot grow without bound.
		n := copy(s.buf, s.buf[s.off:])
		s.buf = s.buf[:n]
		s.off = 0
	}
	s.buf = append(s.buf, b...)
}

func (s *blockSource) reset() {
	s.buf = s.buf[:0]
	s.off = 0
}

func (s *blockSource) Read(p []byte) (int, error) {
	if s.off >= len(s.buf) {
		return 0, io.EOF
	}
	n := copy(p, s.buf[s.off:])
	s.off += n
	return n, nil
}

func (s *blockSource) ReadByte() (byte, error) {
	if s.off >= len(s.buf) {
		return 0, io.EOF
	}
	b := s.buf[s.off]
	s.off++
	return b, nil
}

// zlibStream is one persistent per-connection inflater fed by blockSource.
// Zlib and ZRLE own one each; Tight owns four, selected per rectangle.
type zlibStream struct {
	src blockSource
	r   io.ReadCloser
}

// push hands the stream the next block of compressed bytes.
func (z *zlibStream) push(block []byte) { z.src.push(block) }

// reset discards the inflater and any pending input, so the next block is
// treated as the start of a brand new zlib stream (header and all). This is
// what Tight's stream-reset flags ask for.
func (z *zlibStream) reset() {
	if z.r != nil {
		_ = z.r.Close()
		z.r = nil
	}
	z.src.reset()
}

// readFull decompresses exactly len(buf) bytes.
//
// The inflater is created lazily, on the first read rather than the first
// push, so that a rectangle with no pixels can still deposit its block into
// the stream without forcing the zlib header to be parsed early.
func (z *zlibStream) readFull(buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	if z.r == nil {
		r, err := zlib.NewReader(&z.src)
		if err != nil {
			return fmt.Errorf("start zlib stream: %w", err)
		}
		z.r = r
	}
	if _, err := io.ReadFull(z.r, buf); err != nil {
		return fmt.Errorf("inflate %d bytes: %w", len(buf), err)
	}
	return nil
}

// readCompressedBlock reads a u32-prefixed compressed block from the wire into
// scratch and hands it to z. It returns the (possibly reallocated) scratch
// buffer so the caller can keep reusing it.
func readCompressedBlock(c *Conn, z *zlibStream, scratch []byte) ([]byte, error) {
	var hdr [4]byte
	if err := readFull(c.reader(), hdr[:]); err != nil {
		return scratch, fmt.Errorf("read compressed length: %w", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxCompressedBlock {
		return scratch, fmt.Errorf("implausible compressed length %d", n)
	}
	scratch = growBytes(scratch, int(n))
	if err := readFull(c.reader(), scratch); err != nil {
		return scratch, fmt.Errorf("read %d compressed bytes: %w", n, err)
	}
	z.push(scratch)
	return scratch, nil
}

// ZlibDecoder implements the Zlib encoding (6): the rectangle's Raw pixel data
// run through a single zlib stream shared by the whole connection.
//
// It is the simplest of the compressed encodings and the one that best
// isolates the persistent-stream behaviour, which is why it is worth keeping
// even though Tight and ZRLE beat it on every real workload.
type ZlibDecoder struct {
	z    zlibStream
	comp []byte
	out  []byte
}

// NewZlibDecoder returns a decoder for the Zlib encoding. The returned decoder
// carries a connection-scoped zlib dictionary and must not be shared between
// connections.
func NewZlibDecoder() Decoder { return &ZlibDecoder{} }

// Encoding implements Decoder.
func (d *ZlibDecoder) Encoding() Encoding { return EncodingZlib }

// Decode implements Decoder.
func (d *ZlibDecoder) Decode(c *Conn, r Rect) error {
	comp, err := readCompressedBlock(c, &d.z, d.comp)
	d.comp = comp
	if err != nil {
		return err
	}

	pr := c.PixelReader()
	bpp := pr.BytesPerPixel()
	need := r.Area() * bpp
	if need == 0 {
		// Nothing to inflate, but the block still belongs to the stream and
		// has already been pushed; the next rectangle will consume it.
		return nil
	}
	d.out = growBytes(d.out, need)
	if err := d.z.readFull(d.out); err != nil {
		return err
	}

	fb := c.framebuffer()
	rowBytes := int(r.Width) * bpp
	for y := 0; y < int(r.Height); y++ {
		if err := blitRow(fb, pr, int(r.X), int(r.Y)+y, int(r.Width), d.out[y*rowBytes:]); err != nil {
			return err
		}
	}
	return nil
}
