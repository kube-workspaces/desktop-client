// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package kwclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

// Display roles accepted by the shared display session service.
const (
	DisplayRoleObserver   = "observer"
	DisplayRoleController = "controller"
)

// DisplayCap is the advertised capability of a workspace's shared display
// (GET /v1/workspaces/{name}/display).
type DisplayCap struct {
	Enabled         bool     `json:"enabled"`
	Protocol        int      `json:"protocol"`
	MaxParticipants int      `json:"max_participants"`
	MaxWidth        int      `json:"max_width"`
	MaxHeight       int      `json:"max_height"`
	Transports      []string `json:"transports"`
}

// DisplayParticipant is a member of a workspace's shared display session.
type DisplayParticipant struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Connected bool   `json:"connected"`
	JoinedAt  string `json:"joined_at"`
}

// DisplayStatus is the live membership/control state of a shared display
// (GET /v1/workspaces/{name}/display/status).
type DisplayStatus struct {
	Enabled      bool                 `json:"enabled"`
	Protocol     int                  `json:"protocol"`
	Controller   *DisplayParticipant  `json:"controller,omitempty"`
	Observers    []DisplayParticipant `json:"observers,omitempty"`
	Participants int                  `json:"participants"`
}

// DisplayJoin is the response of a join/member action.
type DisplayJoin struct {
	Participant DisplayParticipant `json:"participant"`
}

// DisplayControl is the response of acquire/release/transfer.
type DisplayControl struct {
	Controller *DisplayParticipant `json:"controller,omitempty"`
	Released   bool                `json:"released"`
	Transfered bool                `json:"transferred"`
}

// Shared-display membership/control sentinels. They wrap the underlying
// [*APIError] and are matched with [errors.Is].
var (
	// ErrDisplayUnavailable reports that shared display sessions are not
	// enabled for the workspace.
	ErrDisplayUnavailable = errors.New("shared display unavailable")
	// ErrControllerPresent reports that the display is controlled by another
	// participant and a plain acquire was refused.
	ErrControllerPresent = errors.New("display already controlled")
	// ErrParticipantNotFound reports an unknown participant id.
	ErrParticipantNotFound = errors.New("display participant not found")
	// ErrNotController reports that the acting participant is not the current
	// controller.
	ErrNotController = errors.New("display participant is not the controller")
)

// sentinelForDisplayStatus maps display REST statuses onto their sentinels.
// sentinelForDisplayStatus maps display REST membership/control statuses onto
// their sentinels for endpoints where the acting participant may be refused a
// role because another participant already holds it (join, acquire).
func sentinelForDisplayStatus(status int) error {
	switch status {
	case http.StatusConflict:
		return ErrControllerPresent
	case http.StatusNotFound:
		return ErrParticipantNotFound
	default:
		return sentinelForStatus(status)
	}
}

// sentinelForDisplayTransition maps control-transition statuses (release,
// transfer) where a 409 means the acting participant is not the current
// controller rather than a takeover being refused.
func sentinelForDisplayTransition(status int) error {
	switch status {
	case http.StatusConflict:
		return ErrNotController
	case http.StatusNotFound:
		return ErrParticipantNotFound
	default:
		return sentinelForStatus(status)
	}
}

// Display returns the capability advertisement for a workspace's shared
// display session.
func (c *Client) Display(ctx context.Context, namespace, name string) (*DisplayCap, error) {
	var out DisplayCap
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodGet,
		path:   workspacePath(name, "display"),
		query:  namespaceQuery(namespace),
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DisplayStatus returns the live membership/control state of a shared display.
func (c *Client) DisplayStatus(ctx context.Context, namespace, name string) (*DisplayStatus, error) {
	var out DisplayStatus
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodGet,
		path:   workspacePath(name, "display", "status"),
		query:  namespaceQuery(namespace),
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// JoinDisplay joins the shared display session as observer or, when free, as
// controller. A controller-occupied display returns [ErrControllerPresent]; joß
// as observer and use [Client.AcquireDisplayControl] to take over.
func (c *Client) JoinDisplay(ctx context.Context, namespace, name, role string) (*DisplayJoin, error) {
	var out DisplayJoin
	if err := c.doJSON(ctx, requestSpec{
		method:   http.MethodPost,
		path:     workspacePath(name, "display", "join"),
		query:    namespaceQuery(namespace),
		body:     map[string]string{"role": role},
		sentinel: sentinelForDisplayStatus,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LeaveDisplay removes the participant from the shared display session.
func (c *Client) LeaveDisplay(ctx context.Context, namespace, name, participantID string) error {
	return c.doJSON(ctx, requestSpec{
		method:   http.MethodDelete,
		path:     workspacePath(name, "display", "sessions", participantID),
		query:    namespaceQuery(namespace),
		sentinel: sentinelForDisplayStatus,
	}, nil)
}

// AcquireDisplayControl promotes the participant (or, with force, takes over
// from the current controller) to controller.
func (c *Client) AcquireDisplayControl(ctx context.Context, namespace, name, participantID string, force bool) (*DisplayControl, error) {
	var out DisplayControl
	if err := c.doJSON(ctx, requestSpec{
		method:   http.MethodPost,
		path:     workspacePath(name, "display", "control", "acquire"),
		query:    namespaceQuery(namespace),
		body:     map[string]any{"participant_id": participantID, "force": force},
		sentinel: sentinelForDisplayStatus,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReleaseDisplayControl releases the controller role back to free.
func (c *Client) ReleaseDisplayControl(ctx context.Context, namespace, name, participantID string) (*DisplayControl, error) {
	var out DisplayControl
	if err := c.doJSON(ctx, requestSpec{
		method:   http.MethodPost,
		path:     workspacePath(name, "display", "control", "release"),
		query:    namespaceQuery(namespace),
		body:     map[string]any{"participant_id": participantID},
		sentinel: sentinelForDisplayTransition,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TransferDisplayControl hands control from the current controller to another
// participant. Without force it fails with [ErrControllerPresent] when the
// target is already a controller.
func (c *Client) TransferDisplayControl(ctx context.Context, namespace, name, participantID, to string, force bool) (*DisplayControl, error) {
	var out DisplayControl
	if err := c.doJSON(ctx, requestSpec{
		method:   http.MethodPost,
		path:     workspacePath(name, "display", "control", "transfer"),
		query:    namespaceQuery(namespace),
		body:     map[string]any{"participant_id": participantID, "to": to, "force": force},
		sentinel: sentinelForDisplayTransition,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// displayWSQueryParam names for the shared display WebSocket bridge.
func displayWSQuery(namespace, participantID, role string, force bool) url.Values {
	q := namespaceQuery(namespace)
	q.Set("participant", participantID)
	q.Set("role", role)
	if force {
		q.Set("force", "1")
	}
	return q
}

// DialDisplayWS opens the shared-display RFB bridge
// (GET /v1/workspaces/{name}/display/ws).
//
// The WebSocket carries raw RFB bytes; the bridge negotiates the same
// subprotocols as the VNC bridge.
//
// A busy display fails with [ErrControllerPresent] unless force is set (the
// participant is already the controller via [Client.AcquireDisplayControl]).
func (c *Client) DialDisplayWS(ctx context.Context, namespace, name, participantID, role string, force bool) (*websocket.Conn, error) {
	conn, _, err := c.DialWS(ctx, workspacePath(name, "display", "ws"), displayWSQuery(namespace, participantID, role, force), vncSubprotocols)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// normalizeRole validates a display role string, returning the canonical value.
func normalizeRole(role string) (string, error) {
	switch role {
	case DisplayRoleObserver, DisplayRoleController:
		return role, nil
	default:
		return "", fmt.Errorf("kwclient: invalid display role %q (want observer or controller)", role)
	}
}
