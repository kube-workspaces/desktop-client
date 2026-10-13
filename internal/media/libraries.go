// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package media provides cgo-free native decoding. Native objects are confined
// to one decoder and serialized with Close; no SDL calls belong here.
package media

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

// ErrUnavailable permits the session selector to retain the universal RFB path.
var ErrUnavailable = errors.New("native media decoder unavailable")

// library owns every binding until the associated decoder has been destroyed.
type library struct{ handle uintptr }

func load(names ...string) (*library, error) {
	// Every candidate is tried because a platform may ship either spelling, but
	// the last failure is kept: "the file is not there" and "the file is there
	// and one of its own dependencies is missing" look identical without it,
	// and those need different fixes.
	var last error
	for _, name := range names {
		h, err := openLibrary(name)
		if err == nil {
			return &library{h}, nil
		}
		last = err
	}
	return nil, fmt.Errorf("%w: could not load %v: %v", ErrUnavailable, names, last)
}

func (l *library) bind(fn any, name string) error {
	addr, err := symbol(l.handle, name)
	if err != nil {
		return fmt.Errorf("%w: missing %s", ErrUnavailable, name)
	}
	purego.RegisterFunc(fn, addr)
	return nil
}

func (l *library) close() {
	if l != nil && l.handle != 0 {
		closeLibrary(l.handle)
		l.handle = 0
	}
}

func codecNames() []string {
	// AVFrame's public prefix below is verified against FFmpeg 5.1 (avutil
	// 57 / avcodec 59). Unknown ABIs must fail before dereferencing native data.
	switch runtime.GOOS {
	case "windows":
		return []string{"avcodec-59.dll"}
	case "darwin":
		return withBundle("libavcodec.59.dylib")
	default:
		return withBundle("libavcodec.so.59")
	}
}

func utilNames() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{"avutil-57.dll"}
	case "darwin":
		return withBundle("libavutil.57.dylib")
	default:
		return withBundle("libavutil.so.57")
	}
}

func opusNames() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{"opus.dll", "libopus-0.dll"}
	case "darwin":
		return withBundle("libopus.0.dylib")
	default:
		return withBundle("libopus.so.0")
	}
}

// bundleDirs reports directories searched before the OS loader paths: the
// executable's own directory (side-by-side DLLs on Windows-style layouts and
// Linux tarballs), a lib/ subdirectory (tarball/RPM layouts), and the macOS
// bundle Frameworks directory. Empty when the executable path cannot be
// determined; the system loader paths are always tried afterwards, so a
// distro-installed library (e.g. a .deb dependency) keeps working and a
// user-supplied build beside the executable always wins.
func bundleDirs() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	dir := filepath.Dir(exe)
	dirs := []string{dir, filepath.Join(dir, "lib")}
	if runtime.GOOS == "darwin" {
		dirs = append(dirs, filepath.Join(dir, "..", "Frameworks"))
	}
	return dirs
}

// withBundle prepends exe-anchored absolute candidates for each bare library
// name, keeping the bare names last for the system loader. Windows is left
// untouched: its safe-DLL-search already resolves the application directory.
func withBundle(names ...string) []string {
	if runtime.GOOS == "windows" {
		return names
	}
	var out []string
	for _, d := range bundleDirs() {
		for _, n := range names {
			out = append(out, filepath.Join(d, n))
		}
	}
	return append(out, names...)
}

// avFramePrefix is the public AVFrame prefix from libavutil 57/frame.h.
// All supported targets use 64-bit pointers; C int remains 32-bit on Windows.
// Never cast an entire AVFrame or depend on sizeof(AVFrame).
type avFramePrefix struct {
	Data                                                  [8]unsafe.Pointer
	Linesize                                              [8]int32
	ExtendedData                                          unsafe.Pointer
	Width, Height, Samples, Format                        int32
	KeyFrame, PictureType                                 int32
	Aspect                                                [2]int32
	PTS, DTS                                              int64
	TimeBase                                              [2]int32
	CodedPicture, DisplayPicture, Quality                 int32
	Opaque                                                unsafe.Pointer
	Repeat, Interlaced, TopFirst, PaletteChanged          int32
	ReorderedOpaque                                       int64
	SampleRate                                            int32
	ChannelLayout                                         uint64
	Buffers                                               [8]unsafe.Pointer
	ExtendedBuffers                                       unsafe.Pointer
	NumExtendedBuffers                                    int32
	SideData                                              unsafe.Pointer
	NumSideData, Flags                                    int32
	ColorRange, ColorPrimaries, ColorTransfer, ColorSpace int32
}
