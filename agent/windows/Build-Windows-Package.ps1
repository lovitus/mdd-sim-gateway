param(
    [Parameter(Mandatory = $true)][string]$HelperDir,
    [string]$OutputDir = "$PSScriptRoot\..\dist\mdd-agent-windows-amd64",
    [switch]$SkipPyInstaller,
    [switch]$Overwrite
)

$ErrorActionPreference = "Stop"
$agentRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$repoRoot = [IO.Path]::GetFullPath((Join-Path $agentRoot ".."))
$agentDist = [IO.Path]::GetFullPath((Join-Path $agentRoot "dist"))
$defaultPackageOutput = [IO.Path]::GetFullPath(
    (Join-Path $agentDist "mdd-agent-windows-amd64"))
$helperRoot = [IO.Path]::GetFullPath($HelperDir)
$output = [IO.Path]::GetFullPath($OutputDir)
$helpers = @("mdd-network-guard.exe", "mdd-windows-mbn.exe", "mdd-call-audio-helper.exe")

if (-not ("MddNativePath" -as [type])) {
    Add-Type -TypeDefinition @"
using System;
using Microsoft.Win32.SafeHandles;
using System.Runtime.InteropServices;
using System.Text;
public static class MddNativePath {
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    public static extern SafeFileHandle CreateFile(
        string path, uint access, uint share, IntPtr security, uint creation,
        uint flags, IntPtr template);
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    public static extern uint GetFinalPathNameByHandle(
        SafeFileHandle handle, StringBuilder path, int length, uint flags);
}
"@
}

function ConvertTo-CanonicalLongPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    $full = [IO.Path]::GetFullPath($Path)
    if ($full.StartsWith('\\', [StringComparison]::Ordinal) -or
        $full -notmatch '^[A-Za-z]:\\') {
        throw "UNC and extended/device package paths are not allowed: $Path"
    }
    $suffix = New-Object 'Collections.Generic.Stack[string]'
    $probe = $full
    while (-not (Test-Path -LiteralPath $probe)) {
        $leaf = [IO.Path]::GetFileName($probe)
        $parent = [IO.Path]::GetDirectoryName($probe)
        if (-not $leaf -or -not $parent -or $parent -eq $probe) {
            throw "Package path has no existing canonical ancestor: $Path"
        }
        $suffix.Push($leaf)
        $probe = $parent
    }
    $handle = [MddNativePath]::CreateFile(
        $probe, 0, 7, [IntPtr]::Zero, 3, 0x02000000, [IntPtr]::Zero)
    if ($null -eq $handle -or $handle.IsInvalid) {
        if ($handle) { $handle.Dispose() }
        throw "Could not open the canonical path ancestor: $probe"
    }
    try {
        $buffer = New-Object Text.StringBuilder 32768
        $length = [MddNativePath]::GetFinalPathNameByHandle(
            $handle, $buffer, $buffer.Capacity, 0)
        if ($length -eq 0 -or $length -ge $buffer.Capacity) {
            throw "Could not resolve the final path for: $probe"
        }
        $canonical = $buffer.ToString()
    } finally {
        $handle.Dispose()
    }
    if ($canonical.StartsWith('\\?\UNC\', [StringComparison]::OrdinalIgnoreCase)) {
        throw "Mapped network package paths are not allowed: $Path"
    }
    if ($canonical.StartsWith('\\?\', [StringComparison]::Ordinal)) {
        $canonical = $canonical.Substring(4)
    }
    if ($canonical -notmatch '^[A-Za-z]:\\') {
        throw "Package path did not resolve to a local drive path: $Path"
    }
    while ($suffix.Count -gt 0) { $canonical = Join-Path $canonical $suffix.Pop() }
    return [IO.Path]::GetFullPath($canonical)
}

function Test-PathEqual {
    param([string]$Left, [string]$Right)
    return [string]::Equals(
        [IO.Path]::GetFullPath($Left).TrimEnd('\'),
        [IO.Path]::GetFullPath($Right).TrimEnd('\'),
        [StringComparison]::OrdinalIgnoreCase)
}

function Test-PathWithin {
    param([string]$Path, [string]$Root)
    $candidate = [IO.Path]::GetFullPath($Path).TrimEnd('\')
    $parent = [IO.Path]::GetFullPath($Root).TrimEnd('\')
    return (Test-PathEqual $candidate $parent) -or
        $candidate.StartsWith($parent + '\', [StringComparison]::OrdinalIgnoreCase)
}

function Assert-NoReparseComponents {
    param([Parameter(Mandatory = $true)][string]$Path)
    $current = [IO.Path]::GetFullPath($Path)
    while ($current) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -LiteralPath $current -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing package output with a reparse path component: $current"
            }
        }
        $parent = [IO.Directory]::GetParent($current)
        if ($null -eq $parent) { break }
        $current = $parent.FullName
    }
}

# Reject reparse aliases on the exact operator-provided/source paths before final-path
# canonicalization resolves them away. Final paths are used only after this lexical gate.
foreach ($rawPath in @($agentRoot, $repoRoot, $agentDist, $helperRoot,
                       $output, $defaultPackageOutput)) {
    Assert-NoReparseComponents -Path $rawPath
}
$agentRoot = ConvertTo-CanonicalLongPath $agentRoot
$repoRoot = ConvertTo-CanonicalLongPath $repoRoot
$agentDist = ConvertTo-CanonicalLongPath $agentDist
$helperRoot = ConvertTo-CanonicalLongPath $helperRoot
$output = ConvertTo-CanonicalLongPath $output
$defaultPackageOutput = ConvertTo-CanonicalLongPath $defaultPackageOutput

function Assert-NoReparseTree {
    param([Parameter(Mandatory = $true)][string]$Path)
    $pending = New-Object 'Collections.Generic.Stack[string]'
    $pending.Push($Path)
    while ($pending.Count -gt 0) {
        $directory = $pending.Pop()
        foreach ($item in (Get-ChildItem -LiteralPath $directory -Force)) {
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing to overwrite a package tree containing a reparse point: $($item.FullName)"
            }
            if ($item.PSIsContainer) { $pending.Push($item.FullName) }
        }
    }
}

function Assert-SafeOutputPath {
    param([Parameter(Mandatory = $true)][string]$Path,
          [Parameter(Mandatory = $true)][string[]]$InputFiles)
    Assert-NoReparseComponents -Path $Path
    foreach ($systemRoot in @($env:windir, $env:ProgramFiles,
                              ${env:ProgramFiles(x86)}, $env:ProgramData)) {
        if ($systemRoot -and
            (Test-PathWithin $Path (ConvertTo-CanonicalLongPath $systemRoot))) {
            throw "Refusing package output inside a protected system tree: $Path"
        }
    }
    if ($env:USERPROFILE -and
        (Test-PathEqual $Path (ConvertTo-CanonicalLongPath $env:USERPROFILE))) {
        throw "Refusing to overwrite the user profile root."
    }
    if (Test-PathWithin $Path $repoRoot) {
        if (-not (Test-PathEqual $Path $defaultPackageOutput)) {
            throw "Only the declared Windows package directory may be overwritten inside the repository: $Path"
        }
    } elseif (Test-PathWithin $repoRoot $Path) {
        throw "Refusing package output that contains the repository source root: $Path"
    }
    if ((Test-PathWithin $Path $helperRoot) -or (Test-PathWithin $helperRoot $Path)) {
        throw "Refusing package output that overlaps HelperDir: $Path"
    }
    foreach ($inputFile in $InputFiles) {
        if ((Test-PathEqual $Path $inputFile) -or (Test-PathWithin $inputFile $Path)) {
            throw "Refusing package output that contains a build input: $inputFile"
        }
    }
    if (Test-Path -LiteralPath $Path) { Assert-NoReparseTree -Path $Path }
}

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

$cli = Join-Path $agentRoot "dist\mdd-agent.exe"
$gui = Join-Path $agentRoot "dist\mdd-agent-gui.exe"
Assert-SafeOutputPath -Path $output -InputFiles @($cli, $gui)

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

foreach ($file in @($cli, $gui)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Missing build output: $file" }
}

if (Test-Path -LiteralPath $output) {
    if (-not $Overwrite) {
        throw "Package output already exists; pass -Overwrite after verifying the exact path: $output"
    }
    Remove-Item -LiteralPath $output -Recurse -Force
}
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
$manifestTool = Join-Path $agentRoot "package_manifest.py"
& python $manifestTool $output --architecture windows-amd64
if ($LASTEXITCODE -ne 0) { throw "Strict package manifest generation failed." }
& python $manifestTool --verify (Join-Path $output "manifest.json") `
    --expect-architecture windows-amd64
if ($LASTEXITCODE -ne 0) { throw "Strict package manifest verification failed." }
Write-Output $output
