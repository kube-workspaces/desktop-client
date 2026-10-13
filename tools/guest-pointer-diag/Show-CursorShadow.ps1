# Temporary cursor-shadow diagnostic for kube-workspaces Tier 1 proof.
# Fullscreen click-through overlay drawing a red ring around the SYSTEM cursor
# position, so the offset between the local pointer and the guest cursor is
# directly visible in the video stream. Inbox .NET only. Self-exits after
# LifetimeSec. Delete after the diagnosis.
# Usage: powershell -ExecutionPolicy Bypass -File Show-CursorShadow.ps1 [-LifetimeSec 180]
param([int]$LifetimeSec = 180)

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type -ReferencedAssemblies System.Windows.Forms,System.Drawing -TypeDefinition @"
using System;
using System.Drawing;
using System.Windows.Forms;
public class ShadowForm : Form {
    public Point CursorAt;
    protected override CreateParams CreateParams {
        get {
            CreateParams cp = base.CreateParams;
            cp.ExStyle |= 0x20;
            return cp;
        }
    }
    protected override void OnPaint(PaintEventArgs e) {
        base.OnPaint(e);
        using (Pen pen = new Pen(Color.Red, 3)) {
            int r = 14;
            e.Graphics.DrawEllipse(pen, CursorAt.X - r, CursorAt.Y - r, r * 2, r * 2);
            e.Graphics.DrawLine(pen, CursorAt.X - r - 6, CursorAt.Y, CursorAt.X + r + 6, CursorAt.Y);
            e.Graphics.DrawLine(pen, CursorAt.X, CursorAt.Y - r - 6, CursorAt.X, CursorAt.Y + r + 6);
        }
    }
}
"@

$form = New-Object ShadowForm
$form.FormBorderStyle = "None"
$form.TopMost = $true
$form.ShowInTaskbar = $false
$form.BackColor = [System.Drawing.Color]::Magenta
$form.TransparencyKey = $form.BackColor
$form.Bounds = [System.Windows.Forms.SystemInformation]::VirtualScreen
$started = Get-Date
$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = 50
$timer.Add_Tick({
    $form.CursorAt = [System.Windows.Forms.Cursor]::Position
    $form.Invalidate()
    if (((Get-Date) - $started).TotalSeconds -gt $LifetimeSec) { $form.Close() }
})
$timer.Start()
[void]$form.ShowDialog()
