# Guest pointer diagnostics (Windows)

Two throwaway PowerShell tools for diagnosing Tier 1 pointer mapping on a
live VM guest, when clicks land somewhere other than the pointer. Inbox .NET
only — no installs, no third party. Both are safe to leave running during a
proof window and delete afterwards.

| Tool | What it does |
|---|---|
| `Show-CursorPos.ps1` | Always-on-top readout of the live system cursor position (`X,Y  DISPLAYn`) plus a low-level mouse hook appending every left-click (`time x y`) to `C:\Windows\Temp\cursors.log`. |
| `Show-CursorShadow.ps1` | Fullscreen click-through overlay drawing a red ring around the system cursor, so the offset between the local pointer and the guest cursor is visible in the stream. Self-exits after `-LifetimeSec` (default 180). |

## Run (in the guest console session)

```powershell
powershell -ExecutionPolicy Bypass -File Show-CursorPos.ps1
powershell -ExecutionPolicy Bypass -File Show-CursorShadow.ps1
```

## Run remotely (operator with cluster access)

The guest runs `qemu-guest-agent` (see the VMI `AgentConnected` condition),
so files and commands go through `virsh qemu-agent-command ... guest-exec`
from the `virt-launcher` pod — no VNC clicking needed for setup:

1. Serve the script from an ephemeral in-cluster file pod and fetch it in
   the guest with `curl.exe` (guest egress required).
2. Session-0 `guest-exec` cannot show UI or read the interactive desktop;
   launch session tools with PsExec (`-i 1`) and collect results through
   files (`Out-File`, log appends), never through `-i` stdout — interactive
   output does not pipe back.
3. `Add-Type -TypeDefinition` C# that touches `System.Windows.Forms` types
   needs `-ReferencedAssemblies System.Windows.Forms,System.Drawing`,
   or compilation fails with "type or namespace name 'Forms' does not exist".

Read results back with `guest-exec` (`type` the log) the same way. Remove
the scripts, logs and PsExec when done; kill leftover PowerShell via
`tasklist`/`taskkill`.

## Calibration procedure

1. Start the readout, park the pointer at each corner of the guest desktop
   as seen in the session window, record the four readout pairs. For a
   `WxH` capture at the virtual origin expect `(0,0)`, `(W-1,0)`,
   `(0,H-1)`, `(W-1,H-1)` — anything else is the transform error, measured.
2. Click known landmarks; the log gives landed coordinates to compare
   against intended ones (distinguishes offset from scale errors).
