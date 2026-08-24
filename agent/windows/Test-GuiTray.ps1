param(
    [Parameter(Mandatory = $true)][string]$ExePath,
    [Parameter(Mandatory = $true)][string]$OutputPath
)

$ErrorActionPreference = "Stop"
$exe = [IO.Path]::GetFullPath($ExePath)
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
    throw "GUI executable not found: $exe"
}

Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class MddGuiTrayTestNative {
    [DllImport("user32.dll")]
    public static extern bool PostMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
    [DllImport("user32.dll")]
    public static extern bool IsWindowVisible(IntPtr h);
}
"@

$before = @(Get-Process mdd-agent-gui -ErrorAction SilentlyContinue |
    ForEach-Object { $_.Id })
$started = @()
$result = $null
try {
    Start-Process -FilePath $exe | Out-Null
    Start-Sleep -Seconds 7
    $started = @(Get-Process mdd-agent-gui -ErrorAction Stop |
        Where-Object { $before -notcontains $_.Id })
    $window = $started | Where-Object { $_.MainWindowHandle -ne 0 } |
        Select-Object -First 1
    if (-not $window) { throw "GUI main window was not created on the interactive desktop." }

    $main = [IntPtr]$window.MainWindowHandle
    [void][MddGuiTrayTestNative]::PostMessage(
        $main, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero) # WM_CLOSE
    Start-Sleep -Seconds 2
    $closeKeptGui = $null -ne (Get-Process -Id $window.Id -ErrorAction SilentlyContinue)
    $hiddenAfterClose = -not [MddGuiTrayTestNative]::IsWindowVisible($main)
    $serviceRunning = [string](Get-Service MddAgent -ErrorAction Stop).Status -eq "Running"
    $result = [ordered]@{
        ok = $closeKeptGui -and $hiddenAfterClose -and $serviceRunning
        processes = $started.Count
        tray_ready_after_close = $closeKeptGui -and $hiddenAfterClose
        close_kept_gui = $closeKeptGui
        hidden_after_close = $hiddenAfterClose
        service_running = $serviceRunning
    }
} catch {
    $result = [ordered]@{ ok = $false; error = $_.Exception.Message }
} finally {
    foreach ($process in $started) {
        $live = Get-CimInstance Win32_Process -Filter "ProcessId=$($process.Id)" `
            -ErrorAction SilentlyContinue
        if ($live -and $live.ExecutablePath -ieq $exe) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        }
    }
    $directory = Split-Path -Parent ([IO.Path]::GetFullPath($OutputPath))
    if ($directory) { New-Item -ItemType Directory -Path $directory -Force | Out-Null }
    $result | ConvertTo-Json -Compress | Set-Content -LiteralPath $OutputPath -Encoding UTF8
}

if (-not $result.ok) { exit 1 }
