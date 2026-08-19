param([switch]$RequireData)
$ErrorActionPreference = "Stop"
$task = Get-ScheduledTask -TaskName "MDDModemAgent" -ErrorAction SilentlyContinue
if (-not $task) {
    Write-Output "MDDModemAgent is not installed; no service was changed."
    exit 0
}
Enable-ScheduledTask -TaskName "MDDModemAgent" | Out-Null
Start-ScheduledTask -TaskName "MDDModemAgent"
$deadline = [DateTime]::UtcNow.AddSeconds(150)
$lastConnection = ""
while ([DateTime]::UtcNow -lt $deadline) {
    $task = Get-ScheduledTask -TaskName "MDDModemAgent"
    if ($task.State -ne "Running") { throw "MDDModemAgent did not remain running." }
    $match = netsh mbn show interfaces | Select-String '^\s+Name\s+:\s+(.+)$' | Select-Object -First 1
    if ($match) {
        $name = $match.Matches[0].Groups[1].Value.Trim()
        $ready = netsh mbn show readyinfo interface="$name" | Out-String
        $lastConnection = netsh mbn show connection interface="$name" | Out-String
        $stackReady = $ready -notmatch 'Stack is off|Device is not ready'
        $dataReady = $lastConnection -match 'Interface State\s+:\s+Connected'
        if ($stackReady -and (-not $RequireData -or $dataReady)) {
            Write-Output "AGENT_PASS"
            Write-Output $lastConnection.Trim()
            exit 0
        }
    }
    Start-Sleep -Seconds 3
}
Write-Output $lastConnection.Trim()
throw "The modem stack did not reach the required post-upgrade state."
