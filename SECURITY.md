# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in this project, please report it responsibly.

**Do NOT open a public GitHub issue for security vulnerabilities.**

Instead, please email security concerns to: security@kubeworkspaces.io

You should receive a response within 72 hours. If the issue is confirmed, we will release a patch as soon as possible depending on complexity.

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest main | Yes |
| Previous releases | Best effort |

This client is in early development and has no released builds yet.

## How the client handles credentials

- **Session tokens are stored in the OS keychain** — macOS Keychain, Windows
  Credential Manager, or the Secret Service / kwallet on Linux. Tokens are
  never written to disk in plaintext, and never to a config file, a log line or
  a crash report.
- **Authentication uses the system browser**, following
  [RFC 8252](https://datatracker.ietf.org/doc/html/rfc8252): the OIDC
  authorization request is opened in the user's real browser, the response comes
  back to a loopback redirect URI on an ephemeral port, and the code exchange is
  protected with PKCE. **No embedded webview is used for authentication** — an
  embedded webview would expose the user's identity-provider credentials to the
  application and defeats the provider's own phishing and device-trust signals.
- Tokens are bearer credentials with a 24 h default lifetime and there is no
  refresh endpoint; the client re-authenticates rather than extending a token.
- The client decodes token claims locally for expiry display only. It cannot and
  does not attempt to validate the token signature — the signing key never
  leaves the server.

## Security Best Practices

When using the desktop client:

- Only connect to instances served over TLS; the client's transport carries both
  the bearer token and the full remote-desktop stream.
- Do not paste session tokens into shell history, scripts or issue reports.
- Treat a workspace console session as you would a physical screen — the display
  transport conveys clipboard contents in both directions.
- Sign out (which removes the token from the keychain) on shared machines.

When deploying Kube Workspaces:

- Always use TLS for ingress
- Rotate the session signing key periodically
- Enable RBAC and limit namespace access
- Keep container images updated
