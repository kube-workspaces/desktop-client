// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WorkspaceType is the kind of workload backing a workspace.
type WorkspaceType string

// Known workspace types.
const (
	// WorkspaceTypeContainer is a pod-backed workspace.
	WorkspaceTypeContainer WorkspaceType = "container"
	// WorkspaceTypeVM is a KubeVirt VirtualMachine-backed workspace; only
	// these expose the console, SSH and VNC bridges.
	WorkspaceTypeVM WorkspaceType = "vm"
	// WorkspaceTypeScratch is an ephemeral workspace.
	WorkspaceTypeScratch WorkspaceType = "scratch"
)

// ContainerState is the observed state of a workspace's main container.
type ContainerState struct {
	// State is "running", "waiting" or "terminated".
	State string `json:"state"`
	// Reason is the short machine reason, e.g. "ImagePullBackOff".
	Reason string `json:"reason,omitempty"`
	// Message is the human-readable detail.
	Message string `json:"message,omitempty"`
	// StartedAt is an RFC 3339 timestamp, when the container has started.
	StartedAt string `json:"started_at,omitempty"`
}

// Condition is a Kubernetes-style status condition on a workspace.
type Condition struct {
	// Type is the condition type, e.g. "Ready".
	Type string `json:"type"`
	// Status is "True", "False" or "Unknown".
	Status string `json:"status"`
	// Reason is the short machine reason.
	Reason string `json:"reason,omitempty"`
	// Message is the human-readable detail.
	Message string `json:"message,omitempty"`
	// LastTransitionTime is an RFC 3339 timestamp.
	LastTransitionTime string `json:"last_transition_time,omitempty"`
}

// VolumeMount is a volume attached to a workspace.
type VolumeMount struct {
	// Name is the volume name.
	Name string `json:"name"`
	// MountPath is the in-workspace path.
	MountPath string `json:"mount_path"`
}

// ImageRemoteDesktop describes an in-guest remote desktop agent.
type ImageRemoteDesktop struct {
	// Protocol name, e.g. "selkies".
	Protocol string `json:"protocol"`
	// Port the agent listens on in the guest.
	Port int `json:"port"`
	// Path relative to the agent's base URL.
	Path *string `json:"path,omitempty"`
}

// Workspace is a workspace as returned by GET /v1/workspaces and
// GET /v1/workspaces/{name}.
//
// The API exposes no URL, link or phase field: connectability is derived from
// Stopped and ReadyReplicas (see [Workspace.Running]) and access URLs are built
// with [Client.ProxyURL].
type Workspace struct {
	// Name is the workspace name.
	Name string `json:"name"`
	// Namespace is the workspace's namespace.
	Namespace string `json:"namespace"`
	// Type is "container", "vm" or "scratch".
	Type WorkspaceType `json:"type"`
	// Image is the container/VM image reference. It is the join key against
	// [Image.Image] in the image catalog.
	Image string `json:"image"`
	// Port is the workspace's primary port, if declared.
	Port *int `json:"port,omitempty"`
	// CPURequest is the CPU request, e.g. "500m".
	CPURequest *string `json:"cpu_request,omitempty"`
	// MemoryRequest is the memory request, e.g. "2Gi".
	MemoryRequest *string `json:"memory_request,omitempty"`
	// CPULimit is the CPU limit.
	CPULimit *string `json:"cpu_limit,omitempty"`
	// MemoryLimit is the memory limit.
	MemoryLimit *string `json:"memory_limit,omitempty"`
	// ReadyReplicas is the number of ready replicas (0 or 1 in practice).
	ReadyReplicas int `json:"ready_replicas"`
	// ContainerState is the main container's observed state, when reported.
	ContainerState *ContainerState `json:"container_state,omitempty"`
	// Conditions are the workspace's status conditions.
	Conditions []Condition `json:"conditions,omitempty"`
	// Stopped reports whether the workspace has been scaled to zero by the
	// user.
	Stopped bool `json:"stopped"`
	// CreatedAt is an RFC 3339 creation timestamp.
	CreatedAt *string `json:"created_at,omitempty"`
	// VolumeMounts are the workspace's volumes.
	VolumeMounts []VolumeMount `json:"volume_mounts,omitempty"`
	// RemoteDesktop is the in-guest agent configuration, if the workspace
	// image supports it.
	RemoteDesktop *ImageRemoteDesktop `json:"remote_desktop,omitempty"`
}

// Running reports whether the workspace can be connected to.
//
// This is the single connectability rule used across the product: a workspace
// is usable when the user has not stopped it and at least one replica is ready.
// There is no phase field to consult.
func (w *Workspace) Running() bool {
	return w != nil && !w.Stopped && w.ReadyReplicas > 0
}

// IsVM reports whether the workspace is VM-backed, i.e. whether the console,
// SSH and VNC endpoints apply to it.
func (w *Workspace) IsVM() bool {
	return w != nil && w.Type == WorkspaceTypeVM
}

// HasTier1 reports whether the workspace image advertises a Tier 1 transport.
func (w *Workspace) HasTier1() bool {
	return w != nil && w.RemoteDesktop != nil && w.RemoteDesktop.Protocol != ""
}

// Key returns "namespace/name", the canonical identifier for a workspace.
func (w *Workspace) Key() string {
	if w == nil {
		return ""
	}
	return w.Namespace + "/" + w.Name
}

// CreatedAtTime parses CreatedAt as RFC 3339. The bool reports whether a valid
// timestamp was present.
func (w *Workspace) CreatedAtTime() (time.Time, bool) {
	if w == nil || w.CreatedAt == nil || *w.CreatedAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, *w.CreatedAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Image is an entry of the workspace image catalog
// (GET /v1/images).
type Image struct {
	// Name is the catalog entry name (the Image CR name).
	Name string `json:"name"`
	// DisplayName is the human-friendly label.
	DisplayName string `json:"display_name"`
	// Image is the image reference, matched against [Workspace.Image].
	Image string `json:"image"`
	// DefaultPath is the HTTP path to open for this image, used to build the
	// proxy URL of a running workspace.
	DefaultPath string `json:"default_path"`
	// DefaultPort is the port the workspace serves on.
	DefaultPort *int `json:"default_port,omitempty"`
	// DefaultUser is the login user, used by the SSH bridge.
	DefaultUser string `json:"default_user"`
	// WorkspaceTypes lists the workspace types this image supports.
	WorkspaceTypes []string `json:"workspace_types"`
	// RemoteDesktop is the in-guest agent configuration, if the image
	// supports it.
	RemoteDesktop *ImageRemoteDesktop `json:"remote_desktop,omitempty"`
}

// SupportsType reports whether the image can back the given workspace type.
// An image with no declared types is treated as unconstrained.
func (i *Image) SupportsType(t WorkspaceType) bool {
	if i == nil {
		return false
	}
	if len(i.WorkspaceTypes) == 0 {
		return true
	}
	for _, wt := range i.WorkspaceTypes {
		if wt == string(t) {
			return true
		}
	}
	return false
}

// ListWorkspaces fetches GET /v1/workspaces?namespace=<ns>.
//
// An empty namespace means [AllNamespaces] ("_all"), the magic value that lists
// across every namespace visible to the caller.
func (c *Client) ListWorkspaces(ctx context.Context, namespace string) ([]Workspace, error) {
	var out []Workspace
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodGet,
		path:   "/v1/workspaces",
		query:  namespaceQuery(namespace),
	}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetWorkspace fetches GET /v1/workspaces/{name}?namespace=<ns>.
//
// A missing workspace yields an [*APIError] wrapping [ErrNotFound]; the
// server's body for that case is a JSON string such as
// "workspace ns/name not found", which ends up in APIError.Message.
func (c *Client) GetWorkspace(ctx context.Context, namespace, name string) (*Workspace, error) {
	var out Workspace
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodGet,
		path:   "/v1/workspaces/" + url.PathEscape(name),
		query:  namespaceQuery(namespace),
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StartWorkspace posts to /v1/workspaces/{name}/start, resuming a stopped
// workspace, and returns the updated workspace.
func (c *Client) StartWorkspace(ctx context.Context, namespace, name string) (*Workspace, error) {
	return c.setStopped(ctx, namespace, name, false)
}

// StopWorkspace posts to /v1/workspaces/{name}/stop, scaling a running
// workspace to zero, and returns the updated workspace.
func (c *Client) StopWorkspace(ctx context.Context, namespace, name string) (*Workspace, error) {
	return c.setStopped(ctx, namespace, name, true)
}

// setStopped runs the lifecycle action. start=false posts start, start=true
// posts stop; both return the workspace the server reports after the change.
func (c *Client) setStopped(ctx context.Context, namespace, name string, stop bool) (*Workspace, error) {
	action := "start"
	if stop {
		action = "stop"
	}
	var out Workspace
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodPost,
		path:   workspacePath(name, action),
		query:  namespaceQuery(namespace),
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListImages fetches GET /v1/images, the catalog of workspace images.
func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	var out []Image
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodGet,
		path:   "/v1/images",
	}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ProxyURL builds the workspace proxy URL
// <base>/proxy/{namespace}/{name}/{path}.
//
// A leading slash on path is stripped so that both "code/" and "/code/" behave
// the same; query strings and fragments in path are passed through untouched.
func (c *Client) ProxyURL(ws Workspace, path string) string {
	u := *c.baseURL
	u.Path = c.baseURL.Path + "/proxy/" + ws.Namespace + "/" + ws.Name + "/"
	// Opaque-append instead of setting u.Path, so that an already-escaped
	// path from the catalog is not double-escaped.
	return u.String() + strings.TrimPrefix(path, "/")
}

// ImageFor returns the catalog entry whose image reference matches the
// workspace's, or nil when the catalog has no entry for it.
func ImageFor(images []Image, ws Workspace) *Image {
	for i := range images {
		if images[i].Image == ws.Image {
			return &images[i]
		}
	}
	return nil
}

// WorkspaceURL builds the URL to open for a workspace, using the catalog
// entry's default path. A nil image falls back to the workspace root.
func (c *Client) WorkspaceURL(ws Workspace, img *Image) string {
	path := ""
	if img != nil {
		path = img.DefaultPath
	}
	return c.ProxyURL(ws, path)
}
