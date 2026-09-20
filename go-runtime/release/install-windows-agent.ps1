param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("Preflight", "Install", "Rollback")]
    [string]$Action,
    [Parameter(Mandatory = $true)][string]$CandidateDirectory,
    [string]$InstallRoot = "$env:ProgramData\MDD\GoAgent",
    [string]$ConfigPath = "$env:ProgramData\MDD\GoAgent\config.json"
)

$ErrorActionPreference = "Stop"
$serviceName = "MddAgent"
$candidate = [IO.Path]::GetFullPath($CandidateDirectory)
$installRoot = [IO.Path]::GetFullPath($InstallRoot)
$configPath = [IO.Path]::GetFullPath($ConfigPath)
$required = @("mdd-agent.exe", "MDD Agent.exe", "mdd-call-audio-helper.exe", "BUILD.txt", "README.txt", "SHA256SUMS")

function Hash([string]$Path) { (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant() }
function Wait-State([string]$State, [int]$Seconds = 45) {
    if ($Seconds -le 0) { throw "service wait timeout must be positive" }
    $service = Get-Service -Name $serviceName -ErrorAction Stop
    try {
        # Bounded ServiceController wait; this still polls inside .NET, but no
        # repeated PowerShell process/service enumeration is performed here.
        $service.WaitForStatus([System.ServiceProcess.ServiceControllerStatus]$State, [TimeSpan]::FromSeconds($Seconds))
    } finally { $service.Dispose() }
}
function Get-ServiceProcess {
    $currentService = Get-CimInstance Win32_Service -Filter "Name='$serviceName'" -ErrorAction Stop
    if (-not $currentService) { throw "service identity is unavailable" }
    $servicePID = [int]$currentService.ProcessId
    if ($servicePID -eq 0) { return $null }
    try { $ownedProcess = Get-Process -Id $servicePID -ErrorAction Stop }
    catch {
        if ((Get-CimInstance Win32_Service -Filter "Name='$serviceName'" -ErrorAction Stop).ProcessId -eq 0) { return $null }
        throw
    }
    # Open the native handle before stop. Waiting on a later name/PID scan can
    # accidentally follow another Agent or a reused PID.
    try {
        $null = $ownedProcess.Handle
        $checkService = Get-CimInstance Win32_Service -Filter "Name='$serviceName'" -ErrorAction Stop
        if (-not $checkService -or [int]$checkService.ProcessId -ne $servicePID) { throw "service process identity changed" }
        return $ownedProcess
    } catch { $ownedProcess.Dispose(); throw }
}
function Wait-AgentExit($OwnedProcess, [int]$Seconds = 45) {
    if (-not $OwnedProcess) { return }
    if ($Seconds -le 0) { throw "process wait timeout must be positive" }
    # Process.WaitForExit waits on the captured process handle, not its name.
    if (-not $OwnedProcess.WaitForExit($Seconds * 1000)) { throw "owned Agent process did not exit" }
}
function Stop-ExactAgent {
    $ownedProcess = Get-ServiceProcess
    try {
        Stop-Service -Name $serviceName -ErrorAction Stop
        Wait-State "Stopped"
        Wait-AgentExit $ownedProcess
    } finally { if ($ownedProcess) { $ownedProcess.Dispose() } }
}
function Assert-RunningAgent([string]$ExpectedPath) {
    $ownedProcess = Get-ServiceProcess
    if (-not $ownedProcess) { throw "running service has no process" }
    try {
        if ((Hash $ownedProcess.Path) -ne (Hash $ExpectedPath)) { throw "running Agent hash mismatch" }
    } finally { $ownedProcess.Dispose() }
}
function Set-ServiceImagePath([string]$Value) {
    & sc.exe config $serviceName binPath= $Value | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "failed to update $serviceName ImagePath" }
}
function Assert-Candidate {
    if (-not (Test-Path -LiteralPath $candidate -PathType Container)) { throw "candidate directory is missing" }
    foreach ($name in $required) {
        if (-not (Test-Path -LiteralPath (Join-Path $candidate $name) -PathType Leaf)) { throw "candidate file is missing: $name" }
    }
    if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) { throw "Agent config is missing" }
    $manifest = Get-Content -Raw -LiteralPath (Join-Path $candidate "SHA256SUMS")
    foreach ($line in ($manifest -split "`r?`n" | Where-Object { $_.Trim() })) {
        $parts = $line -split "\s+", 2
        if ($parts.Count -ne 2) { throw "invalid SHA256SUMS entry" }
        $path = Join-Path $candidate $parts[1].TrimStart("*")
        if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or (Hash $path) -ne $parts[0].ToLowerInvariant()) { throw "candidate hash mismatch: $($parts[1])" }
    }
}
function Assert-ReleaseMatchesCandidate([string]$ReleaseDirectory) {
    $expectedFiles = @(Get-ChildItem -LiteralPath $candidate -Recurse -File | ForEach-Object {
        $_.FullName.Substring($candidate.Length).TrimStart("\")
    } | Sort-Object)
    $actualFiles = @(Get-ChildItem -LiteralPath $ReleaseDirectory -Recurse -File | ForEach-Object {
        $_.FullName.Substring($ReleaseDirectory.Length).TrimStart("\")
    } | Sort-Object)
    if (Compare-Object $expectedFiles $actualFiles) { throw "release file set does not match candidate" }
    foreach ($name in $expectedFiles) {
        if ((Hash (Join-Path $ReleaseDirectory $name)) -ne (Hash (Join-Path $candidate $name))) {
            throw "release file hash mismatch: $name"
        }
    }
}

Assert-Candidate
$build = (Get-Content -LiteralPath (Join-Path $candidate "BUILD.txt") | Where-Object { $_ -match "^source_revision=" }) -replace "^source_revision=", ""
if (-not $build -or $build -notmatch "^[0-9a-f]{40}$") { throw "candidate source revision is missing" }
if ($Action -eq "Preflight") {
    [pscustomobject]@{ status = "preflight_ok"; source_revision = $build; agent_sha256 = Hash (Join-Path $candidate "mdd-agent.exe") } | ConvertTo-Json -Compress
    exit 0
}

$recordRoot = Join-Path $installRoot "deploy-records"
New-Item -ItemType Directory -Force -Path $recordRoot | Out-Null
$record = Join-Path $recordRoot $build

if ($Action -eq "Rollback") {
    $previousImagePathFile = Join-Path $record "previous-image-path.txt"
    if (-not (Test-Path -LiteralPath $previousImagePathFile -PathType Leaf)) { throw "rollback ImagePath receipt is missing" }
    $previousImagePath = (Get-Content -Raw -LiteralPath $previousImagePathFile).Trim()
    if (-not $previousImagePath) { throw "rollback ImagePath receipt is empty" }
    Stop-ExactAgent
    Set-ServiceImagePath $previousImagePath
    Start-Service -Name $serviceName
    Wait-State "Running"
    [pscustomobject]@{ status = "rolled_back"; source_revision = $build } | ConvertTo-Json -Compress
    exit 0
}

$release = Join-Path $installRoot ("releases\" + $build)
if (Test-Path -LiteralPath $release) {
    Assert-ReleaseMatchesCandidate $release
}
$current = (Get-CimInstance Win32_Service -Filter "Name='$serviceName'").PathName
if (-not $current) { throw "current service ImagePath is unavailable" }
New-Item -ItemType Directory -Force -Path $release, $record | Out-Null
$previousImagePathFile = Join-Path $record "previous-image-path.txt"
if (Test-Path -LiteralPath $previousImagePathFile -PathType Leaf) {
    $recordedImagePath = (Get-Content -Raw -LiteralPath $previousImagePathFile).Trim()
    if (-not $recordedImagePath) { throw "recorded rollback ImagePath is empty" }
} elseif ($current -match [regex]::Escape($release)) {
    throw "active release has no rollback ImagePath receipt"
} else {
    Set-Content -LiteralPath $previousImagePathFile -Value $current -NoNewline
}
foreach ($item in Get-ChildItem -LiteralPath $candidate -Force) {
    Copy-Item -LiteralPath $item.FullName -Destination $release -Recurse -Force
}
Assert-ReleaseMatchesCandidate $release
Stop-ExactAgent
try {
    Set-ServiceImagePath ('"{0}" service -config "{1}"' -f (Join-Path $release "mdd-agent.exe"), $configPath)
    Start-Service -Name $serviceName
    Wait-State "Running"
    Assert-RunningAgent (Join-Path $release "mdd-agent.exe")
} catch {
    $deploymentFailure = $_.Exception.Message
    # Never replace ImagePath / restart while candidate ownership is uncertain.
    try { Stop-ExactAgent } catch { throw "rollback blocked because candidate stop is unconfirmed: $($_.Exception.Message); deployment failure: $deploymentFailure" }
    Set-ServiceImagePath $current
    Start-Service -Name $serviceName
    Wait-State "Running"
    throw "deployment rolled back: $deploymentFailure"
}
[pscustomobject]@{ status = "installed"; source_revision = $build; agent_sha256 = Hash (Join-Path $release "mdd-agent.exe") } | ConvertTo-Json -Compress
