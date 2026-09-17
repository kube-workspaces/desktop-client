// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

// Opus decodes plain stereo Opus to 48 kHz signed 16-bit little-endian PCM.
// Returned buffers are Go-owned and may outlive the decoder.
type Opus struct {
	mu      sync.Mutex
	lib     *library
	state   unsafe.Pointer
	decode  func(unsafe.Pointer, []byte, int32, []int16, int32, int32) int32
	destroy func(unsafe.Pointer)
}

func NewOpus() (_ *Opus, err error) {
	d := &Opus{}
	d.lib, err = load(opusNames()...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			d.Close()
		}
	}()
	var create func(int32, int32, *int32) unsafe.Pointer
	for name, fn := range map[string]any{
		"opus_decoder_create": &create, "opus_decoder_destroy": &d.destroy, "opus_decode": &d.decode,
	} {
		if err = d.lib.bind(fn, name); err != nil {
			return nil, err
		}
	}
	var code int32
	d.state = create(48000, 2, &code)
	if d.state == nil || code != 0 {
		return nil, fmt.Errorf("%w: Opus initialization (%d)", ErrUnavailable, code)
	}
	return d, nil
}

func (d *Opus) Decode(packet []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state == nil {
		return nil, errors.New("opus: decoder closed")
	}
	// 1275 bytes per Opus frame; a packet can contain up to 48 frames.
	if len(packet) == 0 || len(packet) > 1275*48 {
		return nil, errors.New("opus: invalid packet size")
	}
	pcm := make([]int16, 5760*2) // maximum 120 ms at 48 kHz, stereo
	n := d.decode(d.state, packet, int32(len(packet)), pcm, 5760, 0)
	if n < 0 {
		return nil, fmt.Errorf("opus: decode failed (%d)", n)
	}
	if n > 5760 {
		return nil, errors.New("opus: invalid sample count")
	}
	out := make([]byte, int(n)*4)
	for i, sample := range pcm[:int(n)*2] {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(sample))
	}
	return out, nil
}

func (d *Opus) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state != nil {
		d.destroy(d.state)
		d.state = nil
	}
	d.lib.close()
}
