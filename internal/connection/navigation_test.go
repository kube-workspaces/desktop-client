// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package connection

import (
	"net/http"
	"strings"
	"testing"
)

func TestNavigationCapabilityAndCommandAllowlist(t *testing.T) {
	commands := make(chan Action, 4)
	n, err := StartNavigation(func(a Action) bool { commands <- a; return true })
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if err := SendNavigation(n.URL, n.Token, Sessions); err != nil {
		t.Fatal(err)
	}
	if got := <-commands; got != Sessions {
		t.Fatal(got)
	}
	for _, test := range []struct {
		token, command, origin string
		status                 int
	}{
		{"wrong", string(Sessions), "", 401}, {n.Token, "disconnect", "", 400}, {n.Token, string(Sessions), "https://workspace.example", 400},
	} {
		req, _ := http.NewRequest(http.MethodPost, n.URL, strings.NewReader(test.command))
		req.Header.Set("Authorization", "Bearer "+test.token)
		req.Header.Set("Origin", test.origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != test.status {
			t.Fatalf("status=%d want=%d", resp.StatusCode, test.status)
		}
	}
	select {
	case <-commands:
		t.Fatal("rejected command reached client")
	default:
	}
	if err := SendNavigation("http://example.com/navigate", n.Token, Sessions); err == nil {
		t.Fatal("non-loopback endpoint accepted")
	}
	n.Close()
	if err := SendNavigation(n.URL, n.Token, Sessions); err == nil {
		t.Fatal("closed capability remained usable")
	}
}
