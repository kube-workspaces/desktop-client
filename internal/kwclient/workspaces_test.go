// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

const workspaceListJSON = `[
  {
    "name": "dev",
    "namespace": "demo",
    "type": "vm",
    "image": "ghcr.io/kube-workspaces/ubuntu:24.04",
    "port": 8080,
    "cpu_request": "500m",
    "memory_request": "2Gi",
    "cpu_limit": "2",
    "memory_limit": "4Gi",
    "ready_replicas": 1,
    "container_state": {
      "state": "running",
      "reason": "",
      "message": "",
      "started_at": "2026-09-12T10:00:00Z"
    },
    "conditions": [
      {
        "type": "Ready",
        "status": "True",
        "reason": "MinimumReplicasAvailable",
        "message": "workspace is ready",
        "last_transition_time": "2026-09-12T10:00:05Z"
      }
    ],
    "stopped": false,
    "created_at": "2026-09-12T09:59:00Z",
    "volume_mounts": [
      {"name": "home", "mount_path": "/home/dev"},
      {"name": "cache", "mount_path": "/var/cache"}
    ]
  },
  {
    "name": "scratchpad",
    "namespace": "ws-ada",
    "type": "scratch",
    "image": "ghcr.io/kube-workspaces/code:latest",
    "ready_replicas": 0,
    "stopped": true
  }
]`

func TestListWorkspacesDecodesAllFields(t *testing.T) {
	var gotQuery, gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		writeJSON(t, w, http.StatusOK, workspaceListJSON)
	})

	list, err := c.ListWorkspaces(context.Background(), "demo")
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if gotPath != "/v1/workspaces" {
		t.Errorf("path = %q, want /v1/workspaces", gotPath)
	}
	if gotQuery != "namespace=demo" {
		t.Errorf("query = %q, want namespace=demo", gotQuery)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}

	ws := list[0]
	if ws.Name != "dev" || ws.Namespace != "demo" || ws.Type != WorkspaceTypeVM {
		t.Errorf("workspace identity = %+v", ws)
	}
	if ws.Image != "ghcr.io/kube-workspaces/ubuntu:24.04" {
		t.Errorf("Image = %q", ws.Image)
	}
	if ws.Port == nil || *ws.Port != 8080 {
		t.Errorf("Port = %v, want 8080", ws.Port)
	}
	for _, f := range []struct {
		name string
		got  *string
		want string
	}{
		{"cpu_request", ws.CPURequest, "500m"},
		{"memory_request", ws.MemoryRequest, "2Gi"},
		{"cpu_limit", ws.CPULimit, "2"},
		{"memory_limit", ws.MemoryLimit, "4Gi"},
	} {
		if f.got == nil || *f.got != f.want {
			t.Errorf("%s = %v, want %q", f.name, f.got, f.want)
		}
	}
	if ws.ReadyReplicas != 1 {
		t.Errorf("ReadyReplicas = %d, want 1", ws.ReadyReplicas)
	}
	if ws.ContainerState == nil || ws.ContainerState.State != "running" ||
		ws.ContainerState.StartedAt != "2026-09-12T10:00:00Z" {
		t.Errorf("ContainerState = %+v", ws.ContainerState)
	}
	if len(ws.Conditions) != 1 {
		t.Fatalf("len(Conditions) = %d, want 1", len(ws.Conditions))
	}
	cond := ws.Conditions[0]
	if cond.Type != "Ready" || cond.Status != "True" ||
		cond.Reason != "MinimumReplicasAvailable" || cond.Message != "workspace is ready" ||
		cond.LastTransitionTime != "2026-09-12T10:00:05Z" {
		t.Errorf("Condition = %+v", cond)
	}
	if ws.Stopped {
		t.Error("Stopped = true, want false")
	}
	if ws.CreatedAt == nil || *ws.CreatedAt != "2026-09-12T09:59:00Z" {
		t.Errorf("CreatedAt = %v", ws.CreatedAt)
	}
	if at, ok := ws.CreatedAtTime(); !ok || at.Year() != 2026 {
		t.Errorf("CreatedAtTime() = %v, %v", at, ok)
	}
	if len(ws.VolumeMounts) != 2 || ws.VolumeMounts[1].Name != "cache" ||
		ws.VolumeMounts[1].MountPath != "/var/cache" {
		t.Errorf("VolumeMounts = %+v", ws.VolumeMounts)
	}
	if !ws.Running() || !ws.IsVM() {
		t.Errorf("Running() = %v, IsVM() = %v, want true, true", ws.Running(), ws.IsVM())
	}
	if got := ws.Key(); got != "demo/dev" {
		t.Errorf("Key() = %q, want demo/dev", got)
	}

	// Optional fields must stay nil rather than decode to zero values.
	second := list[1]
	if second.Port != nil || second.CPURequest != nil || second.ContainerState != nil ||
		second.CreatedAt != nil || second.Conditions != nil || second.VolumeMounts != nil {
		t.Errorf("optional fields should be nil: %+v", second)
	}
	if second.Running() {
		t.Error("stopped workspace must not be Running()")
	}
}

func TestListWorkspacesDefaultsToAllNamespaces(t *testing.T) {
	var gotNamespace string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotNamespace = r.URL.Query().Get("namespace")
		writeJSON(t, w, http.StatusOK, `[]`)
	})

	if _, err := c.ListWorkspaces(context.Background(), ""); err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if gotNamespace != AllNamespaces {
		t.Errorf("namespace = %q, want %q", gotNamespace, AllNamespaces)
	}
	if AllNamespaces != "_all" {
		t.Errorf("AllNamespaces = %q, want _all", AllNamespaces)
	}
}

func TestWorkspaceRunning(t *testing.T) {
	tests := []struct {
		name          string
		stopped       bool
		readyReplicas int
		want          bool
	}{
		{name: "running", stopped: false, readyReplicas: 1, want: true},
		{name: "running with several replicas", stopped: false, readyReplicas: 3, want: true},
		{name: "starting", stopped: false, readyReplicas: 0, want: false},
		{name: "stopped but replica still ready", stopped: true, readyReplicas: 1, want: false},
		{name: "stopped", stopped: true, readyReplicas: 0, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := &Workspace{Stopped: tc.stopped, ReadyReplicas: tc.readyReplicas}
			if got := ws.Running(); got != tc.want {
				t.Errorf("Running() = %v, want %v", got, tc.want)
			}
		})
	}

	var nilWS *Workspace
	if nilWS.Running() || nilWS.IsVM() || nilWS.Key() != "" {
		t.Error("nil workspace methods must be safe and falsy")
	}
}

func TestGetWorkspace(t *testing.T) {
	var gotPath, gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		writeJSON(t, w, http.StatusOK, `{"name":"dev","namespace":"demo","type":"vm","image":"img","ready_replicas":1,"stopped":false}`)
	})

	ws, err := c.GetWorkspace(context.Background(), "demo", "dev")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if gotPath != "/v1/workspaces/dev" {
		t.Errorf("path = %q, want /v1/workspaces/dev", gotPath)
	}
	if gotQuery != "namespace=demo" {
		t.Errorf("query = %q, want namespace=demo", gotQuery)
	}
	if !ws.Running() {
		t.Error("Running() = false, want true")
	}
}

func TestGetWorkspaceNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// The workspace endpoints encode 404 bodies as a JSON string.
		writeJSON(t, w, http.StatusNotFound, `"workspace demo/missing not found"`)
	})

	_, err := c.GetWorkspace(context.Background(), "demo", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As(%v, *APIError) = false", err)
	}
	if apiErr.Message != "workspace demo/missing not found" {
		t.Errorf("Message = %q", apiErr.Message)
	}
}

func TestListImages(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images" {
			t.Errorf("path = %q, want /v1/images", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, `[
			{
				"name": "code-server",
				"display_name": "VS Code",
				"image": "ghcr.io/kube-workspaces/code:latest",
				"default_path": "/?folder=/home/dev",
				"default_port": 8080,
				"default_user": "dev",
				"workspace_types": ["container", "scratch"],
				"icon": "vscode"
			},
			{
				"name": "ubuntu",
				"display_name": "Ubuntu 24.04",
				"image": "ghcr.io/kube-workspaces/ubuntu:24.04",
				"default_path": "",
				"default_user": "ubuntu",
				"workspace_types": ["vm"]
			}
		]`)
	})

	images, err := c.ListImages(context.Background())
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(images) != 2 {
		t.Fatalf("len(images) = %d, want 2", len(images))
	}
	code := images[0]
	if code.Name != "code-server" || code.DisplayName != "VS Code" || code.DefaultUser != "dev" {
		t.Errorf("image = %+v", code)
	}
	if code.DefaultPort == nil || *code.DefaultPort != 8080 {
		t.Errorf("DefaultPort = %v, want 8080", code.DefaultPort)
	}
	if code.DefaultPath != "/?folder=/home/dev" {
		t.Errorf("DefaultPath = %q", code.DefaultPath)
	}
	if !code.SupportsType(WorkspaceTypeContainer) || code.SupportsType(WorkspaceTypeVM) {
		t.Errorf("SupportsType mismatch for %v", code.WorkspaceTypes)
	}
	if images[1].DefaultPort != nil {
		t.Errorf("missing default_port should stay nil, got %v", images[1].DefaultPort)
	}

	ws := Workspace{Name: "dev", Namespace: "demo", Image: "ghcr.io/kube-workspaces/ubuntu:24.04"}
	img := ImageFor(images, ws)
	if img == nil || img.Name != "ubuntu" {
		t.Fatalf("ImageFor = %+v, want the ubuntu entry", img)
	}
	if ImageFor(images, Workspace{Image: "nope"}) != nil {
		t.Error("ImageFor with unknown image = non-nil, want nil")
	}
}

func TestProxyURL(t *testing.T) {
	c, err := New("https://kw.example.com")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ws := Workspace{Name: "dev", Namespace: "demo"}

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "empty path", path: "", want: "https://kw.example.com/proxy/demo/dev/"},
		{name: "root slash", path: "/", want: "https://kw.example.com/proxy/demo/dev/"},
		{name: "leading slash stripped", path: "/code/", want: "https://kw.example.com/proxy/demo/dev/code/"},
		{name: "no leading slash", path: "code/", want: "https://kw.example.com/proxy/demo/dev/code/"},
		{name: "query preserved", path: "/?folder=/home/dev", want: "https://kw.example.com/proxy/demo/dev/?folder=/home/dev"},
		{name: "nested", path: "a/b/c.html", want: "https://kw.example.com/proxy/demo/dev/a/b/c.html"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.ProxyURL(ws, tc.path); got != tc.want {
				t.Errorf("ProxyURL(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}

	t.Run("base URL with sub path", func(t *testing.T) {
		sub, err := New("https://kw.example.com/kw/")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		want := "https://kw.example.com/kw/proxy/demo/dev/code/"
		if got := sub.ProxyURL(ws, "code/"); got != want {
			t.Errorf("ProxyURL = %q, want %q", got, want)
		}
	})

	t.Run("workspace URL from catalog", func(t *testing.T) {
		img := &Image{Image: "img", DefaultPath: "/?folder=/home/dev"}
		want := "https://kw.example.com/proxy/demo/dev/?folder=/home/dev"
		if got := c.WorkspaceURL(ws, img); got != want {
			t.Errorf("WorkspaceURL = %q, want %q", got, want)
		}
		if got := c.WorkspaceURL(ws, nil); got != "https://kw.example.com/proxy/demo/dev/" {
			t.Errorf("WorkspaceURL(nil image) = %q", got)
		}
	})
}

func TestConsoleAndSSHSessionControl(t *testing.T) {
	tests := []struct {
		name     string
		call     func(c *Client) (any, error)
		wantPath string
		wantVerb string
		body     string
	}{
		{
			name: "console status",
			call: func(c *Client) (any, error) {
				return c.ConsoleStatus(context.Background(), "demo", "dev")
			},
			wantPath: "/v1/workspaces/dev/console/status",
			wantVerb: http.MethodGet,
			body:     `{"inUse":true}`,
		},
		{
			name: "console takeover",
			call: func(c *Client) (any, error) {
				return c.ConsoleTakeover(context.Background(), "demo", "dev")
			},
			wantPath: "/v1/workspaces/dev/console/takeover",
			wantVerb: http.MethodPost,
			body:     `{"ok":true,"wasInUse":true}`,
		},
		{
			name: "ssh status",
			call: func(c *Client) (any, error) {
				return c.SSHStatus(context.Background(), "demo", "dev")
			},
			wantPath: "/v1/workspaces/dev/ssh/status",
			wantVerb: http.MethodGet,
			body:     `{"inUse":false}`,
		},
		{
			name: "ssh takeover",
			call: func(c *Client) (any, error) {
				return c.SSHTakeover(context.Background(), "demo", "dev")
			},
			wantPath: "/v1/workspaces/dev/ssh/takeover",
			wantVerb: http.MethodPost,
			body:     `{"ok":true,"wasInUse":false}`,
		},
		{
			name: "vnc status",
			call: func(c *Client) (any, error) {
				return c.VNCStatus(context.Background(), "demo", "dev")
			},
			wantPath: "/v1/workspaces/dev/vnc/status",
			wantVerb: http.MethodGet,
			body:     `{"inUse":true}`,
		},
		{
			name: "vnc status idle",
			call: func(c *Client) (any, error) {
				return c.VNCStatus(context.Background(), "demo", "dev")
			},
			wantPath: "/v1/workspaces/dev/vnc/status",
			wantVerb: http.MethodGet,
			body:     `{"inUse":false}`,
		},
		{
			name: "vnc takeover",
			call: func(c *Client) (any, error) {
				return c.VNCTakeover(context.Background(), "demo", "dev")
			},
			wantPath: "/v1/workspaces/dev/vnc/takeover",
			wantVerb: http.MethodPost,
			body:     `{"ok":true,"wasInUse":true}`,
		},
		{
			name: "vnc takeover idle",
			call: func(c *Client) (any, error) {
				return c.VNCTakeover(context.Background(), "demo", "dev")
			},
			wantPath: "/v1/workspaces/dev/vnc/takeover",
			wantVerb: http.MethodPost,
			body:     `{"ok":true,"wasInUse":false}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotVerb, gotNS string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotVerb = r.URL.Path, r.Method
				gotNS = r.URL.Query().Get("namespace")
				writeJSON(t, w, http.StatusOK, tc.body)
			})

			got, err := tc.call(c)
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if gotPath != tc.wantPath || gotVerb != tc.wantVerb {
				t.Errorf("%s %s, want %s %s", gotVerb, gotPath, tc.wantVerb, tc.wantPath)
			}
			if gotNS != "demo" {
				t.Errorf("namespace = %q, want demo", gotNS)
			}

			switch v := got.(type) {
			case *SessionStatus:
				want := tc.body == `{"inUse":true}`
				if v.InUse != want {
					t.Errorf("InUse = %v, want %v", v.InUse, want)
				}
			case *TakeoverResult:
				if !v.OK {
					t.Errorf("OK = false, want true")
				}
			default:
				t.Fatalf("unexpected result type %T", got)
			}
		})
	}
}

func TestStartStopWorkspace(t *testing.T) {
	tests := []struct {
		name  string
		call  func(c *Client) (*Workspace, error)
		path  string
		state bool // whether the response workspace is stopped
	}{
		{
			name: "start",
			call: func(c *Client) (*Workspace, error) {
				return c.StartWorkspace(context.Background(), "demo", "dev")
			},
			path:  "/v1/workspaces/dev/start",
			state: false,
		},
		{
			name: "stop",
			call: func(c *Client) (*Workspace, error) {
				return c.StopWorkspace(context.Background(), "demo", "dev")
			},
			path:  "/v1/workspaces/dev/stop",
			state: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotVerb, gotNS string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotVerb = r.URL.Path, r.Method
				gotNS = r.URL.Query().Get("namespace")
				writeJSON(t, w, http.StatusOK, `{"name":"dev","namespace":"demo","type":"container","ready_replicas":0,"stopped":`+boolStr(tc.state)+`}`)
			})

			ws, err := tc.call(c)
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if gotVerb != http.MethodPost || gotPath != tc.path {
				t.Errorf("%s %s, want POST %s", gotVerb, gotPath, tc.path)
			}
			if gotNS != "demo" {
				t.Errorf("namespace = %q, want demo", gotNS)
			}
			if ws == nil || ws.Name != "dev" {
				t.Fatalf("workspace = %+v, want the updated workspace", ws)
			}
			if ws.Stopped != tc.state {
				t.Errorf("Stopped = %v, want %v", ws.Stopped, tc.state)
			}
		})
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestStartWorkspaceNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, `"workspace demo/dev not found"`)
	})
	_, err := c.StartWorkspace(context.Background(), "demo", "dev")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v, want 404 APIError", err)
	}
}

func TestReboot(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var gotPath, gotVerb string
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotVerb = r.URL.Path, r.Method
			writeJSON(t, w, http.StatusOK, `{"ok":true}`)
		})
		if err := c.Reboot(context.Background(), "demo", "dev"); err != nil {
			t.Fatalf("Reboot: %v", err)
		}
		if gotVerb != http.MethodPost || gotPath != "/v1/workspaces/dev/reboot" {
			t.Errorf("%s %s, want POST /v1/workspaces/dev/reboot", gotVerb, gotPath)
		}
	})

	t.Run("not a vm", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusBadRequest, `{"error":"reboot is only available for VM workspaces"}`)
		})
		err := c.Reboot(context.Background(), "demo", "dev")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
			t.Fatalf("err = %v, want 400 APIError", err)
		}
		if apiErr.Message != "reboot is only available for VM workspaces" {
			t.Errorf("Message = %q", apiErr.Message)
		}
	})

	t.Run("not acknowledged", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusOK, `{"ok":false}`)
		})
		if err := c.Reboot(context.Background(), "demo", "dev"); err == nil {
			t.Fatal("Reboot: want error when server does not acknowledge")
		}
	})
}

func TestCreateWorkspace(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var gotPath, gotVerb string
		var gotBody map[string]any
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotVerb = r.URL.Path, r.Method
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			writeJSON(t, w, http.StatusCreated, `{"name":"dev","namespace":"demo","type":"container","ready_replicas":0,"stopped":false}`)
		})
		ws, err := c.CreateWorkspace(context.Background(), CreateWorkspacePayload{
			Name:      "dev",
			Namespace: "demo",
			Type:      WorkspaceTypeContainer,
			Container: &WorkspaceContainerSpec{Name: "dev", Image: "example.com/img:latest"},
		})
		if err != nil {
			t.Fatalf("CreateWorkspace: %v", err)
		}
		if gotVerb != http.MethodPost || gotPath != "/v1/workspaces" {
			t.Errorf("%s %s, want POST /v1/workspaces", gotVerb, gotPath)
		}
		if gotBody["name"] != "dev" || gotBody["namespace"] != "demo" || gotBody["type"] != "container" {
			t.Errorf("body = %v, want name/namespace/type", gotBody)
		}
		cont, _ := gotBody["container"].(map[string]any)
		if cont["name"] != "dev" || cont["image"] != "example.com/img:latest" {
			t.Errorf("container = %v, want name/image", cont)
		}
		if _, ok := gotBody["container"].(map[string]any)["port"]; ok {
			t.Errorf("port sent despite being unset; server defaults must apply")
		}
		if ws == nil || ws.Name != "dev" || ws.Namespace != "demo" {
			t.Fatalf("workspace = %+v, want the created workspace", ws)
		}
	})

	t.Run("name taken", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusConflict, `"workspace demo/dev already exists"`)
		})
		_, err := c.CreateWorkspace(context.Background(), CreateWorkspacePayload{Name: "dev"})
		if !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("err = %v, want ErrAlreadyExists", err)
		}
		if errors.Is(err, ErrSessionInUse) {
			t.Fatalf("err = %v, must not be ErrSessionInUse (that is the console 409)", err)
		}
	})

	t.Run("invalid payload", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusBadRequest, `"container is required"`)
		})
		_, err := c.CreateWorkspace(context.Background(), CreateWorkspacePayload{Name: "dev"})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
	})

	t.Run("windows options round trip", func(t *testing.T) {
		var gotBody map[string]any
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			writeJSON(t, w, http.StatusCreated, `{"name":"win","namespace":"demo","type":"vm","ready_replicas":0,"stopped":false}`)
		})
		hostname, root := "talkie", "80Gi"
		_, err := c.CreateWorkspace(context.Background(), CreateWorkspacePayload{
			Name:         "win",
			Namespace:    "demo",
			Type:         WorkspaceTypeVM,
			Container:    &WorkspaceContainerSpec{Name: "win", Image: "registry/private/windows11@sha256:0"},
			VMOptions:    &WorkspaceVMOptions{RootDiskSize: &root},
			NodeSelector: map[string]string{"kubernetes.io/hostname": hostname},
		})
		if err != nil {
			t.Fatalf("CreateWorkspace: %v", err)
		}
		opts, _ := gotBody["vm_options"].(map[string]any)
		if opts["root_disk_size"] != root {
			t.Errorf("vm_options = %v, want root_disk_size %q", opts, root)
		}
		sel, _ := gotBody["node_selector"].(map[string]any)
		if sel["kubernetes.io/hostname"] != hostname {
			t.Errorf("node_selector = %v, want hostname %q", sel, hostname)
		}
	})

	t.Run("windows create omits unset options", func(t *testing.T) {
		var gotBody map[string]any
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			writeJSON(t, w, http.StatusCreated, `{"name":"dev","namespace":"demo","type":"container","ready_replicas":0,"stopped":false}`)
		})
		_, err := c.CreateWorkspace(context.Background(), CreateWorkspacePayload{
			Name:      "dev",
			Namespace: "demo",
			Type:      WorkspaceTypeContainer,
			Container: &WorkspaceContainerSpec{Name: "dev", Image: "example.com/img:latest"},
		})
		if err != nil {
			t.Fatalf("CreateWorkspace: %v", err)
		}
		if _, ok := gotBody["vm_options"]; ok {
			t.Errorf("vm_options sent for a container; server must apply catalog defaults")
		}
		if _, ok := gotBody["node_selector"]; ok {
			t.Errorf("node_selector sent for a container; scheduling stays server-side")
		}
	})
}

func TestWindowsMetadataDecodes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `[
		  {"name":"win","namespace":"demo","type":"vm","image":"registry/private/windows11@sha256:0",
		   "ready_replicas":1,"stopped":false,"vm_profile":"windows11-amd64-v1","provisioned":true,
		   "remote_desktop":{"protocol":"agent","port":44787,"path":"/"}},
		  {"name":"legacy","namespace":"demo","type":"vm","image":"quay.io/vm:old",
		   "ready_replicas":1,"stopped":false}
		]`)
	})
	list, err := c.ListWorkspaces(context.Background(), "demo")
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	win := list[0]
	if !win.IsWindows() {
		t.Errorf("win.IsWindows() = false, want true")
	}
	if !win.IsProvisioned() {
		t.Errorf("win.IsProvisioned() = false, want true")
	}
	if !win.HasAgentTransport() {
		t.Errorf("win.HasAgentTransport() = false, want true")
	}
	if !win.WindowsSignInReady() {
		t.Errorf("win.WindowsSignInReady() = false, want true (provisioned + running)")
	}
	legacy := list[1]
	if legacy.IsWindows() {
		t.Errorf("legacy.IsWindows() = true, want false (nil profile keeps old servers unchanged)")
	}
	if legacy.IsProvisioned() {
		t.Errorf("legacy.IsProvisioned() = true, want false")
	}
	if legacy.WindowsSignInReady() {
		t.Errorf("legacy.WindowsSignInReady() = true, want false")
	}
}

func TestWindowsGatesNeedProfileAndProvisioning(t *testing.T) {
	profile := VMProfileWindows11AMD64V1
	other := "other-profile"
	yes := true
	cases := []struct {
		name        string
		ws          Workspace
		windows     bool
		provisioned bool
		signInReady bool
	}{
		{"nil workspace", Workspace{}, false, false, false},
		{"container with profile", Workspace{Type: WorkspaceTypeContainer, VMProfile: &profile}, false, false, false},
		{"windows vm unprovisioned", Workspace{Type: WorkspaceTypeVM, VMProfile: &profile}, true, false, false},
		{"windows vm provisioned stopped", Workspace{
			Type: WorkspaceTypeVM, VMProfile: &profile, Provisioned: &yes,
			Stopped: true, ReadyReplicas: 1,
		}, true, true, false},
		{"windows vm provisioning", Workspace{
			Type: WorkspaceTypeVM, VMProfile: &profile,
			Stopped: false, ReadyReplicas: 1,
		}, true, false, false},
		{"windows vm ready", Workspace{
			Type: WorkspaceTypeVM, VMProfile: &profile, Provisioned: &yes,
			Stopped: false, ReadyReplicas: 1,
		}, true, true, true},
		{"unknown profile", Workspace{Type: WorkspaceTypeVM, VMProfile: &other, Provisioned: &yes}, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := tc.ws
			if got := ws.IsWindows(); got != tc.windows {
				t.Errorf("IsWindows() = %v, want %v", got, tc.windows)
			}
			if got := ws.IsProvisioned(); got != tc.provisioned {
				t.Errorf("IsProvisioned() = %v, want %v", got, tc.provisioned)
			}
			if got := ws.WindowsSignInReady(); got != tc.signInReady {
				t.Errorf("WindowsSignInReady() = %v, want %v", got, tc.signInReady)
			}
		})
	}
}

func TestImageWindowsFields(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `[
		  {"name":"winimg","display_name":"Windows","image":"registry/private/w@sha256:0",
		   "default_path":"/","default_port":3389,"default_user":"","workspace_types":["vm"],
		   "vm_profile":"windows11-amd64-v1","persistent_root_disk":true,
		   "persistent_root_disk_size":"80Gi","memory_limit":"8Gi","memory_request":"8Gi",
		   "remote_desktop":{"protocol":"agent","port":44787,"path":"/"}},
		  {"name":"plain","display_name":"Plain","image":"example.com/img:latest",
		   "default_path":"/","default_user":"","workspace_types":["container"]}
		]`)
	})
	images, err := c.ListImages(context.Background())
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(images) != 2 {
		t.Fatalf("len(images) = %d, want 2", len(images))
	}
	win := images[0]
	if !win.IsWindowsImage() {
		t.Errorf("IsWindowsImage() = false, want true")
	}
	if win.PersistentRootDisk == nil || !*win.PersistentRootDisk {
		t.Errorf("PersistentRootDisk = %v, want true", win.PersistentRootDisk)
	}
	if win.PersistentRootDiskSize == nil || *win.PersistentRootDiskSize != "80Gi" {
		t.Errorf("PersistentRootDiskSize = %v, want 80Gi", win.PersistentRootDiskSize)
	}
	if images[1].IsWindowsImage() {
		t.Errorf("plain IsWindowsImage() = true, want false")
	}
}

func TestGetWorkspaceCredentials(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var gotPath, gotQuery string
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
			writeJSON(t, w, http.StatusOK, `{"username":"workspace","password":"s3cret!9aA"}`)
		})
		creds, err := c.GetWorkspaceCredentials(context.Background(), "demo", "win")
		if err != nil {
			t.Fatalf("GetWorkspaceCredentials: %v", err)
		}
		if gotPath != "/v1/workspaces/win/credentials" {
			t.Errorf("path = %q, want /v1/workspaces/win/credentials", gotPath)
		}
		if gotQuery != "namespace=demo" {
			t.Errorf("query = %q, want namespace=demo", gotQuery)
		}
		if creds.Username != "workspace" || creds.Password != "s3cret!9aA" {
			t.Errorf("credentials = %+v, want the served pair", creds)
		}
	})

	t.Run("not provisioned", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusServiceUnavailable, `"windows setup is still running"`)
		})
		_, err := c.GetWorkspaceCredentials(context.Background(), "demo", "win")
		if err == nil {
			t.Fatalf("err = nil, want the 503")
		}
	})

	t.Run("forbidden", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusForbidden, `"credentials are not visible to this account"`)
		})
		_, err := c.GetWorkspaceCredentials(context.Background(), "demo", "win")
		if err == nil {
			t.Fatalf("err = nil, want the 403")
		}
	})
}
