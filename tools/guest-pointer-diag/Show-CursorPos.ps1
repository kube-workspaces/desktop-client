# Temporary pointer-mapping diagnostic for kube-workspaces Tier 1 proof.
# Shows a small always-on-top window with the live system cursor position and
# appends every left-button click (time, x, y, monitor) to a log file.
# No install, no third party, pure inbox .NET. Delete after the diagnosis.
# Usage: powershell -ExecutionPolicy Bypass -File Show-CursorPos.ps1 [-Log C:\Windows\Temp\cursors.log]
param([string]$Log = "C:\Windows\Temp\cursors.log")

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

$hookCode = @"
using System;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;
public static class ClickHook {
    private const int WH_MOUSE_LL = 14;
    private const int WM_LBUTTONDOWN = 0x0201;
    public delegate IntPtr HookProc(int nCode, IntPtr wParam, IntPtr lParam);
    private static HookProc proc = HookCallback;
    private static IntPtr hook = IntPtr.Zero;
    public static string LogPath;
    [StructLayout(LayoutKind.Sequential)]
    private struct POINT { public int x; public int y; }
    [StructLayout(LayoutKind.Sequential)]
    private struct MSLLHOOKSTRUCT { public POINT pt; public uint data; public uint flags; public uint time; public IntPtr extra; }
    [DllImport("user32.dll")] private static extern IntPtr SetWindowsHookEx(int id, HookProc lpfn, IntPtr hMod, uint dwThreadId);
    [DllImport("user32.dll")] private static extern bool UnhookWindowsHookEx(IntPtr hhk);
    [DllImport("user32.dll")] private static extern IntPtr CallNextHookEx(IntPtr hhk, int nCode, IntPtr wParam, IntPtr lParam);
    [DllImport("kernel32.dll")] private static extern IntPtr GetModuleHandle(string name);
    public static void Start(string logPath) {
        LogPath = logPath;
        using (Process p = Process.GetCurrentProcess())
        using (ProcessModule m = p.MainModule)
            hook = SetWindowsHookEx(WH_MOUSE_LL, proc, GetModuleHandle(m.ModuleName), 0);
    }
    public static void Stop() { if (hook != IntPtr.Zero) UnhookWindowsHookEx(hook); }
    private static IntPtr HookCallback(int nCode, IntPtr wParam, IntPtr lParam) {
        if (nCode >= 0 && wParam == (IntPtr)WM_LBUTTONDOWN) {
            MSLLHOOKSTRUCT s = (MSLLHOOKSTRUCT)Marshal.PtrToStructure(lParam, typeof(MSLLHOOKSTRUCT));
            try {
                System.IO.File.AppendAllText(LogPath, DateTime.UtcNow.ToString("o") + " click " + s.pt.x + " " + s.pt.y + Environment.NewLine);
            } catch {}
        }
        return CallNextHookEx(hook, nCode, wParam, lParam);
    }
}
"@
Add-Type -TypeDefinition $hookCode

[ClickHook]::Start($Log)

$form = New-Object System.Windows.Forms.Form
$form.Text = "Cursor?"
$form.TopMost = $true
$form.FormBorderStyle = "FixedToolWindow"
$form.StartPosition = "Manual"
$form.Location = New-Object System.Drawing.Point(8, 8)
$form.Size = New-Object System.Drawing.Size(300, 64)
$label = New-Object System.Windows.Forms.Label
$label.Dock = "Fill"
$label.Font = New-Object System.Drawing.Font("Consolas", 14)
$label.TextAlign = "MiddleCenter"
$form.Controls.Add($label)
$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = 100
$timer.Add_Tick({
    $p = [System.Windows.Forms.Cursor]::Position
    $s = [System.Windows.Forms.Screen]::FromPoint($p)
    $label.Text = "$($p.X),$($p.Y)  $($s.DeviceName -replace '.*(DISPLAY\d).*','$1')"
})
$timer.Start()
$form.Add_FormClosed({ $timer.Stop(); [ClickHook]::Stop() })
[void]$form.ShowDialog()
