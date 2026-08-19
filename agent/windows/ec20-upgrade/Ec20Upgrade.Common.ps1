$ErrorActionPreference = "Stop"

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "Run PowerShell as Administrator."
    }
}

function Get-QuectelComPort([string]$namePattern) {
    $matches = @(Get-CimInstance Win32_PnPEntity | Where-Object {
        $_.Name -match $namePattern -and $_.Name -match '\(COM\d+\)'
    })
    if ($matches.Count -ne 1) {
        throw "Expected exactly one $namePattern port; found $($matches.Count). Connect only one module."
    }
    $match = [regex]::Match($matches[0].Name, '\((COM\d+)\)')
    return $match.Groups[1].Value
}

function Invoke-Ec20At([string]$portName, [string[]]$commands) {
    $port = [System.IO.Ports.SerialPort]::new($portName, 115200, "None", 8, "One")
    $port.ReadTimeout = 500
    $port.WriteTimeout = 1000
    $port.DtrEnable = $true
    $port.RtsEnable = $true
    $port.Open()
    $result = [ordered]@{}
    try {
        Start-Sleep -Milliseconds 250
        foreach ($command in $commands) {
            $port.DiscardInBuffer()
            $port.Write("$command`r")
            $deadline = [DateTime]::UtcNow.AddSeconds(8)
            $response = ""
            while ([DateTime]::UtcNow -lt $deadline) {
                $response += $port.ReadExisting()
                if ($response -match '(?m)^(OK|ERROR|\+CME ERROR:.*|\+CMS ERROR:.*)\r?$') {
                    break
                }
                Start-Sleep -Milliseconds 100
            }
            $result[$command] = $response.Trim()
        }
    } finally {
        $port.Close()
        $port.Dispose()
    }
    return $result
}

function Stop-MddModemAgent {
    $task = Get-ScheduledTask -TaskName "MDDModemAgent" -ErrorAction SilentlyContinue
    if ($task) {
        Disable-ScheduledTask -TaskName "MDDModemAgent" | Out-Null
        Stop-ScheduledTask -TaskName "MDDModemAgent" -ErrorAction SilentlyContinue
    }
    Get-Process "mdd-modem-agent" -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Seconds 2
    if (Get-Process "mdd-modem-agent" -ErrorAction SilentlyContinue) {
        throw "MDD Modem Agent is still running and may own a COM port."
    }
}

function Get-Ec20Imei([System.Collections.IDictionary]$responses) {
    $value = [string]$responses["AT+CGSN"]
    $match = [regex]::Match($value, '(?<!\d)(\d{15})(?!\d)')
    if (-not $match.Success) { throw "The modem did not return a 15-digit IMEI." }
    return $match.Groups[1].Value
}

function Write-AtInventory([System.Collections.IDictionary]$responses, [string]$path) {
    $lines = foreach ($entry in $responses.GetEnumerator()) {
        ">>> $($entry.Key)"
        $entry.Value
        ""
    }
    $lines | Out-File -FilePath $path -Encoding utf8
}

function Get-LatestDeviceBackup([string]$deviceBackupRoot) {
    $directories = @(Get-ChildItem $deviceBackupRoot -Directory -ErrorAction SilentlyContinue)
    $timestamped = @($directories | Where-Object { $_.Name -match '^\d{8}-\d{6}$' } |
        Sort-Object Name -Descending)
    if ($timestamped.Count) { return $timestamped[0] }
    return $directories | Sort-Object LastWriteTime -Descending | Select-Object -First 1
}

function Restart-Ec20AfterDiagBackup([string]$portName) {
    $port = [System.IO.Ports.SerialPort]::new($portName, 115200, "None", 8, "One")
    $port.ReadTimeout = 500
    $port.WriteTimeout = 1000
    $port.DtrEnable = $true
    $port.RtsEnable = $true
    try {
        $port.Open()
        $port.Write("AT+CFUN=1,1`r")
        Start-Sleep -Milliseconds 500
    } finally {
        if ($port.IsOpen) { $port.Close() }
        $port.Dispose()
    }
    $deadline = [DateTime]::UtcNow.AddSeconds(90)
    while ([DateTime]::UtcNow -lt $deadline) {
        $at = @(Get-CimInstance Win32_PnPEntity | Where-Object {
            $_.Name -match 'Quectel USB AT Port.*\(COM\d+\)'
        })
        $dm = @(Get-CimInstance Win32_PnPEntity | Where-Object {
            $_.Name -match 'Quectel USB DM Port.*\(COM\d+\)'
        })
        if ($at.Count -eq 1 -and $dm.Count -eq 1) { return }
        Start-Sleep -Seconds 2
    }
    throw "The modem did not re-enumerate its AT and DM ports after DIAG backup."
}
