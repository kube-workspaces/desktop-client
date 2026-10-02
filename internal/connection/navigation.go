// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package connection

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const NavigationURLEnv = "KW_CONNECTION_NAV_URL"
const NavigationTokenEnv = "KW_CONNECTION_NAV_TOKEN"

// Navigation is a child-specific capability to request client navigation. It
// accepts exactly two commands, never workspace operations or shell execution.
// The capability is passed only in the child environment, never page bindings.
type Navigation struct {
	URL, Token string
	server     *http.Server
}

func StartNavigation(deliver func(Action) bool) (*Navigation, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	n := &Navigation{URL: "http://" + ln.Addr().String() + "/navigate", Token: hex.EncodeToString(entropy[:])}
	n.server = &http.Server{ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/navigate" || r.Method != http.MethodPost || r.Header.Get("Origin") != "" {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(token), []byte(n.Token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64))
			if err != nil {
				http.Error(w, "invalid command", http.StatusBadRequest)
				return
			}
			a := Action(string(body))
			if a != Sessions && a != WorkspaceList {
				http.Error(w, "unsupported command", http.StatusBadRequest)
				return
			}
			if !deliver(a) {
				http.Error(w, "client unavailable", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})}
	go func() { _ = n.server.Serve(ln) }()
	return n, nil
}

func (n *Navigation) Close() {
	if n != nil {
		_ = n.server.Close()
	}
}

func SendNavigation(endpoint, token string, a Action) error {
	if a != Sessions && a != WorkspaceList {
		return fmt.Errorf("unsupported navigation command")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "/navigate" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid local navigation endpoint")
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(a)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("local navigation rejected (%d)", resp.StatusCode)
	}
	return nil
}
