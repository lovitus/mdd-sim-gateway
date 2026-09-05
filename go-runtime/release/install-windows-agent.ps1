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
    $deadline = (Get-Date).AddSeconds($Seconds)
    do {
        $service = Get-Service -Name $serviceName -ErrorAction Stop
        if ([string]$service.Status -eq $State) { return }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "$serviceName did not reach $State"
}
function Wait-AgentExit([int]$Seconds = 45) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    do {
        $processes = Get-Process -Name "mdd-agent" -ErrorAction SilentlyContinue
        if (-not $processes) { return }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "mdd-agent process did not exit"
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
    Stop-Service -Name $serviceName
    Wait-State "Stopped"
    Wait-AgentExit
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
Stop-Service -Name $serviceName
Wait-State "Stopped"
Wait-AgentExit
try {
    Set-ServiceImagePath ('"{0}" service -config "{1}"' -f (Join-Path $release "mdd-agent.exe"), $configPath)
    Start-Service -Name $serviceName
    Wait-State "Running"
    if ((Hash (Get-Process -Name "mdd-agent" -ErrorAction Stop | Select-Object -First 1 -ExpandProperty Path)) -ne (Hash (Join-Path $release "mdd-agent.exe"))) { throw "running Agent hash mismatch" }
} catch {
    Stop-Service -Name $serviceName -ErrorAction SilentlyContinue
    Set-ServiceImagePath $current
    Start-Service -Name $serviceName
    Wait-State "Running"
    throw "deployment rolled back: $($_.Exception.Message)"
}
[pscustomobject]@{ status = "installed"; source_revision = $build; agent_sha256 = Hash (Join-Path $release "mdd-agent.exe") } | ConvertTo-Json -Compress
