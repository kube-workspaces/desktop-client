package update

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Exercise real process exit, installed-version execution and relaunch, rather
// than only the injected verifier used by the portable transaction tests.
func TestHelperWaitsSwapsAndRelaunches(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	exe := filepath.Join(dir, ShellBinary)
	put(t, exe, "#!/bin/sh\nprintf 'v1.0.0\\n'\n")
	shell := filepath.Join(work, ShellBinary)
	put(t, shell, "#!/bin/sh\nif [ \"$1\" = version ]; then printf 'v1.1.0\\n'; else printf started > \""+filepath.Join(dir, "started")+"\"; fi\n")
	parent := exec.Command("sleep", "0.1")
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = parent.Wait() }()
	h := handoff{Prepared: Prepared{Tag: "v1.1.0", Work: work, Staged: Staged{Shell: shell}}, Exe: exe, Parent: parent.Process.Pid}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	job := filepath.Join(work, "handoff.json")
	put(t, job, string(b))
	if err := RunHelper(job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new process did not launch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := VerifyVersion(exe, "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exe + BackupSuffix); err != nil {
		t.Fatal("fallback removed before GUI confirmation")
	}
}
