// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

// WatchWorkspaces consumes one authenticated SSE connection. The callback runs
// synchronously on the caller's goroutine for each full list snapshot. Cancel
// ctx to close it; callers own reconnect/backoff and polling fallback.
func (c *Client) WatchWorkspaces(ctx context.Context, namespace string, snapshot func([]Workspace)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	spec := requestSpec{method: http.MethodGet, path: "/v1/workspaces/watch", query: namespaceQuery(namespace)}
	req, err := c.newRequest(ctx, spec)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	// REST's total request timeout would truncate a healthy stream every 30s.
	// Keep the shared transport/settings but bound idle time (including headers).
	httpc := *c.httpc
	httpc.Timeout = 0
	idle := time.AfterFunc(65*time.Second, cancel)
	defer idle.Stop()
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("kwclient: %s: %w", spec.op(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return newAPIError(spec.op(), resp, sentinelForStatus)
	}
	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if contentType != "text/event-stream" {
		return fmt.Errorf("kwclient: watch: unexpected content type %q", contentType)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	var event string
	var data strings.Builder
	for scanner.Scan() {
		idle.Reset(65 * time.Second)
		line := scanner.Text()
		if line == "" {
			if event == "snapshot" {
				workspaces, err := decodeWorkspaceSnapshot([]byte(data.String()))
				if err != nil {
					return fmt.Errorf("kwclient: watch snapshot: %w", err)
				}
				snapshot(workspaces)
			}
			event = ""
			data.Reset()
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			if data.Len()+len(value)+1 > 8<<20 {
				return fmt.Errorf("kwclient: watch snapshot too large")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("kwclient: watch: %w", err)
	}
	return io.EOF
}

func decodeWorkspaceSnapshot(raw []byte) ([]Workspace, error) {
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, err
	}
	// encoding/json accepts Name for a name tag, but not ReadyReplicas for
	// ready_replicas. Reject service-model JSON instead of silently rendering
	// all workspaces unready; the shell will use its normal list fallback.
	for i, record := range records {
		for _, field := range []string{"name", "namespace", "type", "image", "ready_replicas", "stopped"} {
			value, ok := record[field]
			if !ok || strings.TrimSpace(string(value)) == "null" {
				return nil, fmt.Errorf("workspace %d missing %s", i, field)
			}
		}
	}
	var workspaces []Workspace
	if err := json.Unmarshal(raw, &workspaces); err != nil {
		return nil, err
	}
	return workspaces, nil
}
