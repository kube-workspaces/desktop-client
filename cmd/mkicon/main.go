// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Command mkicon derives the platform icon assets from the master brand mark.
//
// It reads assets/icon.png (the 1024×1024 cube, rasterised from assets/icon.svg,
// see `make icons`) and writes:
//
//   - assets/icon.icns — the macOS icon, a set of PNG chunks in an icns
//     container, one per size with the type code macOS expects for it;
//   - internal/viewer/icon.png — the 256×256 copy embedded into the binary and
//     shown on the window/title-bar through SDL_SetWindowIcon.
//
// The .ico for Windows is produced by ImageMagick (`make icons`), which
// embeds true 32-bit DIB entries that the .icns/PNG shortcut cannot match.
//
// Everything here is stdlib-only so the outputs can be regenerated on any
// host that checked the tree out — no macOS tools required.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
)

// icnsType maps a nominal size to the icon type code whose PNG payload macOS
// expects. The plain (non-@2x) codes are sufficient for the Dock and Finder;
// the Retina variants are served by the same PNGs at larger nominal sizes.
var icnsType = map[int]string{
	16:   "icp4",
	32:   "icp5",
	64:   "icp6",
	128:  "ic07",
	256:  "ic08",
	512:  "ic09",
	1024: "ic10",
}

func main() {
	src := flag.String("src", "assets/icon.png", "master logo PNG, square and preferably 1024×1024")
	flag.Parse()
	if err := run(*src); err != nil {
		fmt.Fprintln(os.Stderr, "mkicon:", err)
		os.Exit(1)
	}
}

func run(src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		return fmt.Errorf("decode %s: %w", src, err)
	}
	b := img.Bounds()
	if b.Dx() != b.Dy() {
		return fmt.Errorf("%s is not square (%dx%d)", src, b.Dx(), b.Dy())
	}
	base := toNRGBA(img)

	var sizes []int
	for s := range icnsType {
		sizes = append(sizes, s)
	}
	// Stable, ascending chunk order.
	for i := 0; i < len(sizes); i++ {
		for j := i + 1; j < len(sizes); j++ {
			if sizes[j] < sizes[i] {
				sizes[i], sizes[j] = sizes[j], sizes[i]
			}
		}
	}

	icnsData, err := buildICNS(base, sizes)
	if err != nil {
		return err
	}
	icnsPath := filepath.Join(filepath.Dir(src), "icon.icns")
	if err := os.WriteFile(icnsPath, icnsData, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d bytes)\n", icnsPath, len(icnsData))

	embed := resizeBox(base, 256, 256)
	embedPath := filepath.Join("internal", "viewer", "icon.png")
	embedBytes, err := encodePNG(embed)
	if err != nil {
		return err
	}
	if err := os.WriteFile(embedPath, embedBytes, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d bytes)\n", embedPath, len(embedBytes))
	return nil
}

// buildICNS packs a PNG-encoded icon per size into an icns container. Every
// chunk is size (PNG), type (the code macOS selects the right size from) and
// length-prefixed; the whole file is "icns" followed by its big-endian length.
func buildICNS(base *image.NRGBA, sizes []int) ([]byte, error) {
	var body bytes.Buffer
	for _, s := range sizes {
		pngBytes, err := encodePNG(resizeBox(base, s, s))
		if err != nil {
			return nil, err
		}
		body.WriteString(icnsType[s])
		var ln [4]byte
		binary.BigEndian.PutUint32(ln[:], uint32(8+len(pngBytes)))
		body.Write(ln[:])
		body.Write(pngBytes)
	}
	var header [8]byte
	copy(header[:4], "icns")
	binary.BigEndian.PutUint32(header[4:8], uint32(8+body.Len()))
	return append(header[:], body.Bytes()...), nil
}
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func toNRGBA(img image.Image) *image.NRGBA {
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// resizeBox downscales src to dw×dh by averaging every source pixel inside
// each destination pixel's footprint. It is a proper area filter, so a thin
// stroked logo stays legible at 16×16 instead of turning into nearest-neighbour
// Moiré. Source and destination are covered by the -src flag's square
// guarantee, but the code does not depend on it.
func resizeBox(src *image.NRGBA, dw, dh int) *image.NRGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	rAcc := make([]uint64, dw)
	gAcc := make([]uint64, dw)
	bAcc := make([]uint64, dw)
	aAcc := make([]uint64, dw)
	nAcc := make([]uint64, dw)
	for y := 0; y < dh; y++ {
		y0 := y * sh / dh
		y1 := (y + 1) * sh / dh
		if y1 <= y0 {
			y1 = y0 + 1
			if y1 > sh {
				y1 = sh
			}
		}
		clear(rAcc)
		clear(gAcc)
		clear(bAcc)
		clear(aAcc)
		clear(nAcc)
		for sy := y0; sy < y1; sy++ {
			srcRow := src.Pix[sy*src.Stride:]
			for x := 0; x < dw; x++ {
				x0 := x * sw / dw
				x1 := (x + 1) * sw / dw
				if x1 <= x0 {
					x1 = x0 + 1
					if x1 > sw {
						x1 = sw
					}
				}
				var r, g, bl, a, n uint64
				for sx := x0; sx < x1; sx++ {
					p := srcRow[sx*4 : sx*4+4]
					r += uint64(p[0])
					g += uint64(p[1])
					bl += uint64(p[2])
					a += uint64(p[3])
					n++
				}
				rAcc[x] += r
				gAcc[x] += g
				bAcc[x] += bl
				aAcc[x] += a
				nAcc[x] += n
			}
		}
		dstRow := dst.Pix[y*dst.Stride:]
		for x := 0; x < dw; x++ {
			if nAcc[x] == 0 {
				continue
			}
			p := dstRow[x*4 : x*4+4]
			p[0] = uint8(rAcc[x] / nAcc[x])
			p[1] = uint8(gAcc[x] / nAcc[x])
			p[2] = uint8(bAcc[x] / nAcc[x])
			p[3] = uint8(aAcc[x] / nAcc[x])
		}
	}
	return dst
}
