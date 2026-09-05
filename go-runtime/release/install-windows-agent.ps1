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

Assert-Candidate
$build = (Get-Content -LiteralPath (Join-Path $candidate "BUILD.txt") | Where-Object { $_ -match "^source_revision=" }) -replace "^source_revision=", ""
if (-not $build -or $build -notmatch "^[0-9a-f]{40}$") { throw "candidate source revision is missing" }
$recordRoot = Join-Path $installRoot "deploy-records"
New-Item -ItemType Directory -Force -Path $recordRoot | Out-Null
$record = Join-Path $recordRoot $build

if ($Action -eq "Preflight") {
    [pscustomobject]@{ status = "preflight_ok"; source_revision = $build; agent_sha256 = Hash (Join-Path $candidate "mdd-agent.exe") } | ConvertTo-Json -Compress
    exit 0
}

if ($Action -eq "Rollback") {
    $previous = Join-Path $record "previous"
    if (-not (Test-Path -LiteralPath $previous -PathType Container)) { throw "rollback release is missing" }
    Wait-State "Stopped"
    Copy-Item -LiteralPath (Join-Path $previous "mdd-agent.exe") -Destination (Join-Path $installRoot "mdd-agent.exe") -Force
    Start-Service -Name $serviceName
    Wait-State "Running"
    [pscustomobject]@{ status = "rolled_back"; source_revision = $build } | ConvertTo-Json -Compress
    exit 0
}

$release = Join-Path $installRoot ("releases\" + $build)
if (Test-Path -LiteralPath $release) { throw "release already exists: $build" }
$current = (Get-CimInstance Win32_Service -Filter "Name='$serviceName'").PathName
New-Item -ItemType Directory -Force -Path $release, $record | Out-Null
$previous = Join-Path $record "previous"
New-Item -ItemType Directory -Force -Path $previous | Out-Null
$existingReleases = Get-ChildItem -LiteralPath (Join-Path $installRoot "releases") -Directory -ErrorAction SilentlyContinue
if ($existingReleases) {
    $currentRelease = $existingReleases | Sort-Object LastWriteTime -Descending | Select-Object -First 1
    Copy-Item -LiteralPath $currentRelease.FullName -Destination $previous -Recurse -Force
}
Copy-Item -LiteralPath (Join-Path $candidate "*") -Destination $release -Recurse -Force
Stop-Service -Name $serviceName
Wait-State "Stopped"
try {
    Set-ItemProperty -LiteralPath "HKLM:\SYSTEM\CurrentControlSet\Services\$serviceName" -Name ImagePath -Value ('"{0}" service run' -f (Join-Path $release "mdd-agent.exe"))
    Start-Service -Name $serviceName
    Wait-State "Running"
    if ((Hash (Get-Process -Name "mdd-agent" -ErrorAction Stop | Select-Object -First 1 -ExpandProperty Path)) -ne (Hash (Join-Path $release "mdd-agent.exe"))) { throw "running Agent hash mismatch" }
} catch {
    Stop-Service -Name $serviceName -ErrorAction SilentlyContinue
    Set-ItemProperty -LiteralPath "HKLM:\SYSTEM\CurrentControlSet\Services\$serviceName" -Name ImagePath -Value $current
    Start-Service -Name $serviceName
    Wait-State "Running"
    throw "deployment rolled back: $($_.Exception.Message)"
}
[pscustomobject]@{ status = "installed"; source_revision = $build; agent_sha256 = Hash (Join-Path $release "mdd-agent.exe") } | ConvertTo-Json -Compress
