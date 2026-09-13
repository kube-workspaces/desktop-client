// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"testing"
)

func TestResizeBox(t *testing.T) {
	// A uniform image stays uniform at any size, proving the averages are
	// normalised rather than accumulated.
	src := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < 8*8; i++ {
		src.Pix[i*4+0] = 10
		src.Pix[i*4+1] = 20
		src.Pix[i*4+2] = 30
		src.Pix[i*4+3] = 255
	}
	small := resizeBox(src, 2, 2)
	if small.Rect.Dx() != 2 || small.Rect.Dy() != 2 {
		t.Fatalf("resizeBox to 2x2 = %dx%d", small.Rect.Dx(), small.Rect.Dy())
	}
	for i := 0; i < 2*2; i++ {
		p := small.Pix[i*4 : i*4+4]
		if p[0] != 10 || p[1] != 20 || p[2] != 30 || p[3] != 255 {
			t.Fatalf("pixel %d = %v, want [10 20 30 255]", i, p)
		}
	}
	// Downscaling below one source pixel per destination pixel must still
	// produce a valid image of the requested size.
	tiny := resizeBox(src, 1, 1)
	if tiny.Rect.Dx() != 1 || tiny.Rect.Dy() != 1 {
		t.Fatalf("resizeBox to 1x1 = %dx%d", tiny.Rect.Dx(), tiny.Rect.Dy())
	}
}

func TestBuildICNS(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for i := range src.Pix {
		src.Pix[i] = 0x80
	}
	data, err := buildICNS(src, []int{16, 32})
	if err != nil {
		t.Fatalf("buildICNS: %v", err)
	}
	if string(data[:4]) != "icns" {
		t.Fatalf("header magic = %q, want icns", data[:4])
	}
	total := int(binary.BigEndian.Uint32(data[4:8]))
	if total != len(data) {
		t.Fatalf("stated length %d != actual %d", total, len(data))
	}

	var got [][2]int // type code, nominal size
	pos := 8
	for pos < total {
		typ := string(data[pos : pos+4])
		ln := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		if pos+ln > total {
			t.Fatalf("chunk %s overruns the container (%d > %d)", typ, pos+ln, total)
		}
		img, err := png.Decode(bytes.NewReader(data[pos+8 : pos+ln]))
		if err != nil {
			t.Fatalf("chunk %s is not a PNG: %v", typ, err)
		}
		b := img.Bounds()
		got = append(got, [2]int{0, b.Dx()})
		pos += ln
	}
	if got[0][1] != 16 || got[1][1] != 32 {
		t.Fatalf("chunk sizes = %v, want [16 32]", got)
	}
}
