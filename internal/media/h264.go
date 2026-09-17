// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package media

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"unsafe"
)

// H264 accepts complete Annex-B access units, in order. Decode drains all
// output and returns the newest frame, discarding only decoded presentation
// frames, never encoded reference frames. Close and Decode are serialized.
type H264 struct {
	mu                 sync.Mutex
	codec, util        *library
	ctx, packet, frame unsafe.Pointer
	freeContext        func(*unsafe.Pointer)
	freePacket         func(*unsafe.Pointer)
	freeFrame          func(*unsafe.Pointer)
	unrefPacket        func(unsafe.Pointer)
	unrefFrame         func(unsafe.Pointer)
	alloc              func(uintptr) unsafe.Pointer
	free               func(unsafe.Pointer)
	fromData           func(unsafe.Pointer, unsafe.Pointer, int32) int32
	send               func(unsafe.Pointer, unsafe.Pointer) int32
	receive            func(unsafe.Pointer, unsafe.Pointer) int32
	flush              func(unsafe.Pointer)
}

func NewH264() (_ *H264, err error) {
	d := &H264{}
	defer func() {
		if err != nil {
			d.Close()
		}
	}()
	if d.codec, err = load(codecNames()...); err != nil {
		return nil, err
	}
	if d.util, err = load(utilNames()...); err != nil {
		return nil, err
	}
	var codecVersion, utilVersion func() uint32
	if err = d.codec.bind(&codecVersion, "avcodec_version"); err != nil {
		return nil, err
	}
	if err = d.util.bind(&utilVersion, "avutil_version"); err != nil {
		return nil, err
	}
	if codecVersion()>>16 != 59 || utilVersion()>>16 != 57 {
		return nil, fmt.Errorf("%w: unsupported FFmpeg ABI", ErrUnavailable)
	}
	var find func(int32) unsafe.Pointer
	var allocContext func(unsafe.Pointer) unsafe.Pointer
	var open func(unsafe.Pointer, unsafe.Pointer, unsafe.Pointer) int32
	var allocPacket, allocFrame func() unsafe.Pointer
	var setInt func(unsafe.Pointer, string, int64, int32) int32
	for name, fn := range map[string]any{
		"avcodec_find_decoder": &find, "avcodec_alloc_context3": &allocContext,
		"avcodec_open2": &open, "avcodec_free_context": &d.freeContext,
		"av_packet_alloc": &allocPacket, "av_packet_free": &d.freePacket,
		"av_packet_unref": &d.unrefPacket, "av_packet_from_data": &d.fromData,
		"avcodec_send_packet": &d.send, "avcodec_receive_frame": &d.receive,
		"avcodec_flush_buffers": &d.flush,
	} {
		if err = d.codec.bind(fn, name); err != nil {
			return nil, err
		}
	}
	for name, fn := range map[string]any{
		"av_frame_alloc": &allocFrame, "av_frame_free": &d.freeFrame,
		"av_frame_unref": &d.unrefFrame, "av_mallocz": &d.alloc,
		"av_free": &d.free, "av_opt_set_int": &setInt,
	} {
		if err = d.util.bind(fn, name); err != nil {
			return nil, err
		}
	}
	codec := find(27) // AV_CODEC_ID_H264
	if codec == nil {
		return nil, fmt.Errorf("%w: H.264 codec missing", ErrUnavailable)
	}
	d.ctx, d.packet, d.frame = allocContext(codec), allocPacket(), allocFrame()
	if d.ctx == nil || d.packet == nil || d.frame == nil {
		return nil, errors.New("h264: allocation failed")
	}
	// Bound native allocations before parsing an untrusted SPS. Single-thread
	// slice decoding avoids FFmpeg's automatic frame-thread latency/queues.
	if setInt(d.ctx, "max_pixels", 4096*4096, 0) < 0 || setInt(d.ctx, "threads", 1, 0) < 0 {
		return nil, fmt.Errorf("%w: FFmpeg resource limits unavailable", ErrUnavailable)
	}
	if code := open(d.ctx, codec, nil); code < 0 {
		return nil, fmt.Errorf("h264: open failed (%d)", code)
	}
	return d, nil
}

func (d *H264) Decode(accessUnit []byte) (*image.RGBA, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx == nil {
		return nil, errors.New("h264: decoder closed")
	}
	if len(accessUnit) == 0 || len(accessUnit) > 8*1024*1024 {
		return nil, errors.New("h264: invalid access unit size")
	}
	// libavcodec may overread by AV_INPUT_BUFFER_PADDING_SIZE (64). The
	// zero-padded native allocation becomes the AVPacket's owned buffer.
	buf := d.alloc(uintptr(len(accessUnit) + 64))
	if buf == nil {
		return nil, errors.New("h264: packet allocation failed")
	}
	copy(unsafe.Slice((*byte)(buf), len(accessUnit)), accessUnit)
	if code := d.fromData(d.packet, buf, int32(len(accessUnit))); code < 0 {
		d.free(buf)
		return nil, fmt.Errorf("h264: packet initialization failed (%d)", code)
	}
	defer d.unrefPacket(d.packet)
	if code := d.send(d.ctx, d.packet); code < 0 {
		return nil, fmt.Errorf("h264: send failed (%d)", code)
	}
	var latest *image.RGBA
	for {
		code := d.receive(d.ctx, d.frame)
		if code == -11 || code == -35 {
			return latest, nil
		} // EAGAIN Linux/Windows, Darwin
		if code < 0 {
			return nil, fmt.Errorf("h264: receive failed (%d)", code)
		}
		frame, err := copyFrame((*avFramePrefix)(d.frame))
		d.unrefFrame(d.frame)
		if err != nil {
			return nil, err
		}
		latest = frame
	}
}

// Reset discards codec references. The next access unit must be a keyframe
// carrying SPS/PPS; callers must not resume from a predicted frame.
func (d *H264) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx != nil {
		d.flush(d.ctx)
	}
}

func (d *H264) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.frame != nil {
		d.freeFrame(&d.frame)
	}
	if d.packet != nil {
		d.freePacket(&d.packet)
	}
	if d.ctx != nil {
		d.freeContext(&d.ctx)
	}
	d.codec.close()
	d.util.close()
}

func copyFrame(f *avFramePrefix) (*image.RGBA, error) {
	w, h := int(f.Width), int(f.Height)
	if w < 1 || h < 1 || w > 4096 || h > 4096 || (f.Format != 0 && f.Format != 12) {
		return nil, errors.New("h264: expected bounded YUV420P frame")
	}
	if f.ColorRange < 0 || f.ColorRange > 2 {
		return nil, errors.New("h264: unsupported color range")
	}
	// BT.601 (also the unspecified-stream fallback) and BT.709. Reject HDR
	// and other matrices rather than silently presenting incorrect colors.
	rv, gu, gv, bu := 91881, 22554, 46802, 116130
	switch f.ColorSpace {
	case 1:
		rv, gu, gv, bu = 103206, 12276, 30679, 121609
	case 2, 5, 6:
	default:
		return nil, errors.New("h264: unsupported color matrix")
	}
	var planes [3][]byte
	for i := range planes {
		pw, ph := w, h
		if i > 0 {
			pw, ph = (w+1)/2, (h+1)/2
		}
		stride := int(f.Linesize[i])
		if f.Data[i] == nil || stride < pw || stride > 16384 {
			return nil, errors.New("h264: invalid plane stride")
		}
		planes[i] = unsafe.Slice((*byte)(f.Data[i]), stride*(ph-1)+pw)
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			yy := int(planes[0][y*int(f.Linesize[0])+x])
			cb := int(planes[1][y/2*int(f.Linesize[1])+x/2]) - 128
			cr := int(planes[2][y/2*int(f.Linesize[2])+x/2]) - 128
			if f.ColorRange != 2 && f.Format != 12 {
				yy, cb, cr = (yy-16)*255/219, cb*255/224, cr*255/224
			}
			r := clamp(yy + (rv * cr >> 16))
			g := clamp(yy - ((gu*cb + gv*cr) >> 16))
			b := clamp(yy + (bu * cb >> 16))
			i := y*out.Stride + x*4
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = r, g, b, 255
		}
	}
	return out, nil
}

func clamp(v int) uint8 { return uint8(max(0, min(255, v))) }
