// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSecondLaunchActivatesOwner(t *testing.T) {
	dir := t.TempDir()
	owner, err := Acquire(context.Background(), dir)
	if err != nil || owner == nil {
		t.Fatalf("first launch: %v, %v", owner, err)
	}
	defer owner.Close()
	activated := make(chan struct{}, 1)
	owner.Start(func() { activated <- struct{}{} })
	second, err := Acquire(context.Background(), dir)
	if err != nil || second != nil {
		t.Fatalf("second launch: %v, %v", second, err)
	}
	select {
	case <-activated:
	case <-time.After(time.Second):
		t.Fatal("owner was not activated")
	}
}

func TestSimultaneousLaunchesHaveOneOwner(t *testing.T) {
	dir := t.TempDir()
	const launches = 8
	var wg sync.WaitGroup
	owners := make(chan *Guard, launches)
	activated := make(chan struct{}, launches)
	for range launches {
		wg.Go(func() {
			g, err := Acquire(context.Background(), dir)
			if err != nil {
				t.Errorf("launch: %v", err)
				return
			}
			if g != nil {
				g.Start(func() { activated <- struct{}{} })
				owners <- g
			}
		})
	}
	wg.Wait()
	close(owners)
	count := 0
	for g := range owners {
		g.Close()
		count++
	}
	if count != 1 || len(activated) != launches-1 {
		t.Fatalf("owners=%d, activations=%d", count, len(activated))
	}
}

func TestActivationRequiresToken(t *testing.T) {
	g, err := Acquire(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	activated := make(chan struct{}, 1)
	g.Start(func() { activated <- struct{}{} })
	c, err := net.Dial("tcp4", g.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	_, _ = fmt.Fprintln(c, "wrong token")
	var ack [3]byte
	if n, _ := c.Read(ack[:]); n != 0 {
		t.Fatal("unauthenticated activation was acknowledged")
	}
	if len(activated) != 0 {
		t.Fatal("unauthenticated activation reached UI")
	}
}

func TestOwnerStartupWaitCanBeCanceled(t *testing.T) {
	dir := t.TempDir()
	g, err := Acquire(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, dir); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting for owner startup: %v", err)
	}
}

func TestCrashReleasesLock(t *testing.T) {
	const env = "KW_TEST_INSTANCE_CRASH_DIR"
	if dir := os.Getenv(env); dir != "" {
		g, err := Acquire(context.Background(), dir)
		if err != nil || g == nil {
			os.Exit(2)
		}
		os.Exit(0) // Deliberately skip Close, as a crash would.
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashReleasesLock$")
	cmd.Env = append(os.Environ(), env+"="+dir)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v: %s", err, data)
	}
	// Confirm there really is stale metadata before claiming the released lock.
	data, err := os.ReadFile(filepath.Join(dir, "shell-instance.lock"))
	var old endpoint
	if err != nil || json.Unmarshal(data, &old) != nil || old.PID == 0 {
		t.Fatalf("missing stale endpoint: %s, %v", data, err)
	}
	g, err := Acquire(context.Background(), dir)
	if err != nil || g == nil {
		t.Fatalf("launch after crash: %v, %v", g, err)
	}
	g.Close()
}
