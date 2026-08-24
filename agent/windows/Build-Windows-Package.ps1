param(
    [Parameter(Mandatory = $true)][string]$HelperDir,
    [string]$OutputDir = "$PSScriptRoot\..\dist\mdd-agent-windows-amd64",
    [switch]$SkipPyInstaller
)

$ErrorActionPreference = "Stop"
$agentRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$helperRoot = [IO.Path]::GetFullPath($HelperDir)
$output = [IO.Path]::GetFullPath($OutputDir)
$helpers = @("mdd-network-guard.exe", "mdd-windows-mbn.exe", "mdd-call-audio-helper.exe")

function Assert-CallAudioHelperProtocol {
    param([Parameter(Mandatory = $true)][string]$Path)
    $raw = @(& $Path -mode list)
    if ($LASTEXITCODE -ne 0) {
        throw "Call-audio helper protocol check failed with exit code $LASTEXITCODE."
    }
    try { $value = ($raw -join "`n") | ConvertFrom-Json } catch {
        throw "Call-audio helper protocol check did not return valid JSON."
    }
    $okType = if ($null -eq $value.ok) { "" } else { $value.ok.GetType().FullName }
    $versionType = if ($null -eq $value.version) { "" } else {
        $value.version.GetType().FullName
    }
    if ($okType -ne "System.Boolean" -or $value.ok -ne $true -or
        $versionType -notin @("System.Int32", "System.Int64") -or
        [long]$value.version -lt 2) {
        throw "Call-audio helper protocol v2 or newer is required."
    }
}

foreach ($file in $helpers) {
    if (-not (Test-Path -LiteralPath (Join-Path $helperRoot $file) -PathType Leaf)) {
        throw "Missing prebuilt release helper: $file"
    }
}
Assert-CallAudioHelperProtocol -Path (Join-Path $helperRoot "mdd-call-audio-helper.exe")

if (-not $SkipPyInstaller) {
    Push-Location $agentRoot
    try {
        & python -m PyInstaller --noconfirm --clean "mdd-agent.spec"
        if ($LASTEXITCODE -ne 0) { throw "PyInstaller failed for mdd-agent.exe" }
        & python -m PyInstaller --noconfirm --clean "mdd-agent-gui.spec"
        if ($LASTEXITCODE -ne 0) { throw "PyInstaller failed for mdd-agent-gui.exe" }
    } finally {
        Pop-Location
    }
}

$cli = Join-Path $agentRoot "dist\mdd-agent.exe"
$gui = Join-Path $agentRoot "dist\mdd-agent-gui.exe"
foreach ($file in @($cli, $gui)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Missing build output: $file" }
}

if (Test-Path -LiteralPath $output) { Remove-Item -LiteralPath $output -Recurse -Force }
New-Item -ItemType Directory -Path $output -Force | Out-Null
Copy-Item -LiteralPath $cli, $gui -Destination $output
foreach ($file in $helpers) {
    Copy-Item -LiteralPath (Join-Path $helperRoot $file) -Destination $output
}
$gammu = Join-Path $helperRoot "gammu.exe"
if (Test-Path -LiteralPath $gammu -PathType Leaf) {
    Copy-Item -LiteralPath $gammu -Destination $output
}
Copy-Item -LiteralPath (Join-Path $agentRoot "MODEM_AGENT.md") -Destination $output

$manifest = Get-ChildItem -LiteralPath $output -File | Sort-Object Name | ForEach-Object {
    $hash = Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256
    [ordered]@{ name = $_.Name; bytes = $_.Length; sha256 = $hash.Hash.ToLowerInvariant() }
}
[ordered]@{ version = 1; architecture = "windows-amd64"; files = @($manifest) } |
    ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $output "manifest.json") -Encoding UTF8
Write-Output $output
