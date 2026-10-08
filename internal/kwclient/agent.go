package kwclient

// Agent transport API calls: short-lived attach tickets for the guest-agent
// bridge served by the proxy at /proxy/{namespace}/{name}/agent/.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

// Agent errors surface the handshake outcome (401/403/404/409/503) before any
// media flows, mirroring the bridge-failure contract: inspect the handshake,
// not the post-upgrade stream.
var (
	ErrAgentBusy         = fmt.Errorf("kwclient: agent session busy")
	ErrAgentUnconfigured = fmt.Errorf("kwclient: agent transport not configured for this image")
	ErrAgentTicketDenied = fmt.Errorf("kwclient: agent ticket refused")
)

// AgentTicket is a short-lived attach grant minted by the API.
type AgentTicket struct {
	ID       string `json:"id"`
	Ticket   string `json:"ticket"`
	TTLMs    int    `json:"ttl_ms"`
	Protocol int    `json:"protocol"`
}

// AgentAttach mints an attach ticket for a workspace's guest agent session.
func (c *Client) AgentAttach(ctx context.Context, namespace, name, participant string) (*AgentTicket, error) {
	var out AgentTicket
	body := map[string]string{}
	if participant != "" {
		body["participant"] = participant
	}
	if err := c.doJSON(ctx, requestSpec{
		method:   http.MethodPost,
		path:     workspacePath(name, "agent", "attach"),
		query:    namespaceQuery(namespace),
		body:     body,
		sentinel: agentSentinel,
	}, &out); err != nil {
		return nil, err
	}
	if out.Protocol != 1 || out.ID == "" || out.Ticket == "" || out.TTLMs <= 0 {
		return nil, fmt.Errorf("kwclient: malformed agent ticket from API")
	}
	return &out, nil
}

// AgentPath constructs the proxy agent-bridge endpoint for one workspace.
// Same conservative validation as SelkiesPath: DNS labels only, so no
// encoding, traversal or authority syntax can redirect the credential.
func AgentPath(namespace, name string) (string, error) {
	if !dnsLabel(namespace) || !dnsLabel(name) {
		return "", fmt.Errorf("kwclient: agent bridge requires DNS-label namespace and workspace name")
	}
	return "/proxy/" + namespace + "/" + name + "/agent/", nil
}

// AgentStatus reports live agent session state for a workspace (the API leg
// of the indicator contract: clients combine it with proxy counters and
// guest telemetry, never alone).
type AgentStatus struct {
	Active   bool `json:"active"`
	Sessions int  `json:"sessions"`
}

// AgentSessionStatus reports whether any live agent session is bound to
// the workspace.
func (c *Client) AgentSessionStatus(ctx context.Context, namespace, name string) (*AgentStatus, error) {
	var out AgentStatus
	if err := c.doJSON(ctx, requestSpec{
		method:   http.MethodGet,
		path:     workspacePath(name, "agent", "status"),
		query:    namespaceQuery(namespace),
		sentinel: agentSentinel,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AgentRenew extends a live agent session by its server-side id.
func (c *Client) AgentRenew(ctx context.Context, namespace, name, sessionID string) error {
	var out struct {
		TTLMs    int `json:"ttl_ms"`
		Protocol int `json:"protocol"`
	}
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodPost,
		path:   workspacePath(name, "agent", "renew"),
		query:  namespaceQuery(namespace),
		// The API requires a JSON body (Goa rejects bodiless POSTs with
		// missing-payload); the id rides in the body AND the header.
		body:     map[string]string{"session_id": sessionID},
		headers:  map[string]string{"X-KW-Agent-Session": sessionID},
		sentinel: agentSentinel,
	}, &out); err != nil {
		return err
	}
	return nil
}

// AgentRelease revokes an agent session id (best-effort; unknown ids report ok).
func (c *Client) AgentRelease(ctx context.Context, namespace, name, sessionID string) error {
	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.doJSON(ctx, requestSpec{
		method: http.MethodPost,
		path:   workspacePath(name, "agent", "release"),
		query:  namespaceQuery(namespace),
		// JSON body required (see AgentRenew); id in body and header.
		body:     map[string]string{"session_id": sessionID},
		headers:  map[string]string{"X-KW-Agent-Session": sessionID},
		sentinel: agentSentinel,
	}, &out); err != nil {
		return err
	}
	return nil
}

func agentSentinel(status int) error {
	switch status {
	case http.StatusConflict:
		return ErrAgentBusy
	case http.StatusServiceUnavailable:
		return ErrAgentUnconfigured
	case http.StatusForbidden:
		return ErrAgentTicketDenied
	default:
		return sentinelForStatus(status)
	}
}

// DialAgentWS opens the proxied agent bridge (/proxy/{ns}/{name}/agent/).
// The stream carries framed-protocol bytes 1:1 in binary messages; no
// subprotocol is negotiated (an empty subprotocol list is deliberate).
func (c *Client) DialAgentWS(ctx context.Context, namespace, name string) (*websocket.Conn, *http.Response, error) {
	path, err := AgentPath(namespace, name)
	if err != nil {
		return nil, nil, err
	}
	u := *c.baseURL
	u.Path = c.baseURL.Path + path
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	header := http.Header{}
	header.Set("User-Agent", c.userAgent)
	c.authenticate(header)
	dialer := &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: c.handshakeTimeout,
		TLSClientConfig:  c.tlsConfig(),
	}
	conn, resp, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusConflict:
				return nil, resp, ErrAgentBusy
			case http.StatusServiceUnavailable:
				return nil, resp, ErrAgentUnconfigured
			case http.StatusForbidden:
				return nil, resp, ErrAgentTicketDenied
			}
		}
		return nil, resp, err
	}
	return conn, resp, nil
}

// AgentProxyURL builds the ws(s) URL for the agent bridge (diagnostics).
func (c *Client) AgentProxyURL(namespace, name string) string {
	u := *c.baseURL
	u.Path = c.baseURL.Path + "/proxy/" + namespace + "/" + name + "/agent/"
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	_ = url.Values{}
	return u.String()
}
