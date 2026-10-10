// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package rfb

import (
	"bufio"
	"bytes"
	"testing"
)

func TestStatsCountsBytesWrittenAtSend(t *testing.T) {
	var wire bytes.Buffer
	c := &Conn{
		counting: &countingReader{r: bytes.NewReader(nil)},
		w:        bufio.NewWriter(&wire),
		fb:       NewFramebuffer(8, 8),
	}
	payloads := [][]byte{{1, 2, 3}, make([]byte, 100)}
	total := 0
	for _, p := range payloads {
		if err := c.send(p); err != nil {
			t.Fatalf("send: %v", err)
		}
		total += len(p)
	}
	if wire.Len() != total {
		t.Fatalf("wire = %d bytes, want %d", wire.Len(), total)
	}
	stats := c.Stats()
	if stats.BytesWritten != uint64(total) {
		t.Fatalf("BytesWritten = %d, want %d", stats.BytesWritten, total)
	}
	if stats.BytesRead != 0 {
		t.Fatalf("BytesRead = %d, want 0 (nothing received)", stats.BytesRead)
	}
}
