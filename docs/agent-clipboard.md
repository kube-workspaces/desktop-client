# Guest text clipboard through the agent

The guest agent can share Unicode text clipboard data while the normal
RFB console continues to provide keyboard, mouse and display. This is
an opt-in development feature and requires a matching agent/client build.

In the **logged-in Windows guest console**, start:

```powershell
kw-agent.exe serve --workspace-uid <uid> --workspace-generation <generation> --bind 0.0.0.0 --port 44787 --clipboard
```

No video/audio flags are needed for clipboard-only use. The Windows
clipboard backend runs in this user's session, not a SYSTEM service.

On the host, fully exit any previous graphical client process, then start:

```powershell
kube-workspaces.exe shell --agent-clipboard
```

Open the VM normally. The display stays **RFB**; clipboard text goes through
the separately authenticated agent bridge. Copy on the host, then paste
with Ctrl+V in the guest; copy guest command output to bring it to the host
clipboard. The existing clipboard-sync toggle applies. Observers do not
acquire this helper. Disconnect/sign-out/client exit release both seats.
Closing/parking a session window retains its held session until Disconnect.

The agent bridge is exclusive: a premium agent window, clipboard helper or
diagnostic already holding it prevents another attachment. No automatic
takeover occurs. The guest must advertise `clipboardText`; old agents or
agents without `--clipboard` return an explicit unsupported error.

## One-shot command beside VNC

The CLI uses the saved instance profile and login. It requires no video
decoder libraries and can run while a VNC console is open, but not while
another agent attachment holds the seat:

```sh
kube-workspaces clipboard my-vm --read
kube-workspaces clipboard my-vm --write < command.txt
```

Read writes exact text to stdout without appending a newline. Write reads
UTF-8 from stdin, preserving supplied newlines. On Windows PowerShell 7:

```powershell
Get-Clipboard -Raw | .\kube-workspaces.exe clipboard my-vm --write
.\kube-workspaces.exe clipboard my-vm --read | Set-Clipboard
```

PowerShell pipelines can add/change newlines; Windows PowerShell 5.1 may
also use a non-UTF-8 pipeline encoding. Prefer GUI sync for exact Unicode
content, or UTF-8 file redirection from a shell that preserves bytes.

## Premium presentation diagnostic

`agent-probe --present --duration 20s` opens the real SDL premium viewer
instead of only decoding bytes. It reports uploaded/presented frames and
PCM queued to the playback backend, and releases the agent ticket when the
window/duration ends. Like the headless probe, it reads `KW_SESSION` and
requires `--server`, `--namespace` and `--workspace`.

The agent premium viewer currently supports video, playback audio and
negotiated text clipboard. Keyboard/pointer injection and applying guest
resize are still pending. Unsupported input is not forwarded and does not
terminate the view-only stream; local fullscreen/disconnect controls work.
Use RFB with `--agent-clipboard` when interactive keyboard/mouse is needed.
The toolbar does not advertise guest resize before it can apply a mode.

On Windows, supported native decoder DLLs (FFmpeg avcodec 59/avutil 57 and
Opus, including their dependencies) must be beside the executable. Unknown
FFmpeg ABIs fail preflight. The diagnostic does not install or bundle codecs.

Only plain text is supported, up to 64 KiB UTF-8, without embedded NUL.
Empty text is supported. Files, images, and automatic typing/execution are
not clipboard operations. Clipboard contents are not written to logs.

Offline Unicode, empty-text, limits, admission, polling/echo suppression,
renewal and release checks cover the protocol path. Real Windows clipboard
and native GUI acceptance must also be recorded before claiming delivery.
