param(
    [ValidateSet("Prepare", "Install", "Uninstall")][string]$Action = "Install",
    [Parameter(Mandatory = $true)][string]$BinaryPath,
    [string]$DataDir = "$env:ProgramData\MDD\Agent",
    [switch]$ReaderOnly,
    [switch]$AllowLegacyMaintenancePreflight,
    [switch]$PurgeData
)

$ErrorActionPreference = "Stop"
$serviceName = "MddAgent"
$legacyTaskName = "MDDModemAgent"
$installRoot = Join-Path $env:ProgramFiles "MDD\Agent"
$sourceRoot = Split-Path -Parent ([IO.Path]::GetFullPath($BinaryPath))
$requiredFiles = @("mdd-agent-gui.exe", "mdd-network-guard.exe",
                   "mdd-windows-mbn.exe", "mdd-call-audio-helper.exe",
                   "MODEM_AGENT.md", "manifest.json")
$requiredPackageFiles = @($requiredFiles) + @("control-agent-allowlist.env")
$requiredManifestFiles = @("mdd-agent.exe", "mdd-agent-gui.exe",
                           "mdd-network-guard.exe", "mdd-windows-mbn.exe",
                           "mdd-call-audio-helper.exe", "MODEM_AGENT.md")
$allowedManifestFiles = @($requiredManifestFiles) + @("gammu.exe")

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw "Administrator privileges are required. Re-run in an elevated PowerShell."
    }
}

function Invoke-NativeChecked {
    param([Parameter(Mandatory = $true)][string]$FilePath,
          [Parameter(Mandatory = $true)][string[]]$Arguments,
          [int[]]$AllowedExitCodes = @(0))
    $capture = Invoke-NativeCaptured -Path $FilePath -Arguments $Arguments
    $code = $capture.ExitCode
    if ($AllowedExitCodes -notcontains $code) {
        throw "$FilePath failed with exit code $code (arguments: $($Arguments -join ' '))"
    }
}

function Set-ProtectedAcl {
    param([Parameter(Mandatory = $true)][string]$Path, [bool]$AllowUsersRead = $false)
    $arguments = @($Path, "/inheritance:r", "/grant:r",
                   "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F")
    if ($AllowUsersRead) { $arguments += "*S-1-5-32-545:(OI)(CI)RX" }
    Invoke-NativeChecked -FilePath "icacls.exe" -Arguments $arguments
    $acl = Get-Acl -LiteralPath $Path
    $trusted = @("S-1-5-18", "S-1-5-32-544")
    $writeMask = (
        [Security.AccessControl.FileSystemRights]::WriteData -bor
        [Security.AccessControl.FileSystemRights]::AppendData -bor
        [Security.AccessControl.FileSystemRights]::WriteExtendedAttributes -bor
        [Security.AccessControl.FileSystemRights]::WriteAttributes -bor
        [Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles -bor
        [Security.AccessControl.FileSystemRights]::Delete -bor
        [Security.AccessControl.FileSystemRights]::ChangePermissions -bor
        [Security.AccessControl.FileSystemRights]::TakeOwnership)
    foreach ($rule in $acl.Access) {
        $sid = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        $dangerous = ($rule.FileSystemRights -band $writeMask) -ne 0
        if ($rule.AccessControlType -eq "Allow" -and $dangerous -and $trusted -notcontains $sid) {
            throw "Unsafe writable ACL remains on $Path for SID $sid"
        }
    }
}

function Wait-ServiceState {
    param([string]$Name, [string]$State, [int]$Seconds = 45)
    $deadline = (Get-Date).AddSeconds($Seconds)
    do {
        $service = Get-Service -Name $Name -ErrorAction Stop
        if ([string]$service.Status -eq $State) { return }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "$Name did not reach service state $State (current=$($service.Status))."
}

function Stop-ServiceBounded {
    param([string]$Name, [int]$Seconds = 45)
    $deadline = (Get-Date).AddSeconds($Seconds)
    do {
        $service = Get-Service -Name $Name -ErrorAction SilentlyContinue
        if (-not $service -or [string]$service.Status -eq "Stopped") { return }
        if ([string]$service.Status -notin @("StopPending", "StartPending")) {
            Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("stop", $Name) `
                -AllowedExitCodes @(0, 1061, 1062)
        }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "$Name did not stop before the rollback deadline."
}

function Wait-ServiceAbsent {
    param([string]$Name, [int]$Seconds = 30)
    $deadline = (Get-Date).AddSeconds($Seconds)
    do {
        if ($null -eq (Get-Service -Name $Name -ErrorAction SilentlyContinue)) { return }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "$Name service object was not deleted before the rollback deadline."
}

function Wait-AgentLeaseReleased {
    param([int]$Seconds = 90)
    $deadline = (Get-Date).AddSeconds($Seconds)
    do {
        $lease = $null
        try {
            $lease = [Threading.Mutex]::OpenExisting("Global\MDDUnifiedAgent-v1")
        } catch [Threading.WaitHandleCannotBeOpenedException] {
            return
        } catch [UnauthorizedAccessException] {
            # A lease that exists but cannot be inspected is still unsafe to race.  Keep
            # waiting and fail closed at the bounded deadline.
        } finally {
            if ($lease) { $lease.Dispose() }
        }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "The previous MDD Agent process did not release its installation lease."
}

function Assert-CallAudioHelperProtocol {
    param([Parameter(Mandatory = $true)][string]$Path)
    $capture = Invoke-NativeCaptured -Path $Path -Arguments @("-mode", "list")
    $raw = @($capture.Output)
    if ($capture.ExitCode -ne 0) {
        throw "Call-audio helper protocol check failed with exit code $($capture.ExitCode)."
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

function Test-ExactPropertySet {
    param([Parameter(Mandatory = $true)]$Value,
          [Parameter(Mandatory = $true)][string[]]$Names)
    if ($null -eq $Value -or $null -eq $Value.PSObject) { return $false }
    $expected = New-Object 'Collections.Generic.HashSet[string]' `
        ([StringComparer]::Ordinal)
    foreach ($name in $Names) { [void]$expected.Add($name) }
    $observed = @($Value.PSObject.Properties | Select-Object -ExpandProperty Name)
    if ($observed.Count -ne $expected.Count) { return $false }
    foreach ($name in $observed) {
        if (-not $expected.Contains([string]$name)) { return $false }
    }
    return $true
}

function Test-NativeJsonInteger {
    param($Value)
    if ($null -eq $Value) { return $false }
    return $Value.GetType().FullName -in @("System.Int32", "System.Int64")
}

function Assert-NoReparseComponents {
    param([Parameter(Mandatory = $true)][string]$Path)
    $current = [IO.Path]::GetFullPath($Path)
    while ($current) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -LiteralPath $current -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Package path contains a reparse point: $current"
            }
        }
        $parent = [IO.Directory]::GetParent($current)
        if ($null -eq $parent) { break }
        $current = $parent.FullName
    }
}

function Invoke-NativeCaptured {
    param([Parameter(Mandatory = $true)][string]$Path,
          [string[]]$Arguments = @())
    # Windows PowerShell 5 promotes a native process' stderr to a non-terminating
    # NativeCommandError.  With this installer's fail-fast preference that error
    # would abort before we can inspect the process exit code and distinguish a
    # modern structured refusal from a supervised legacy CLI.  Limit the relaxed
    # preference to the capture itself; callers still validate the result strictly.
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf) -and
        -not (Get-Command $Path -CommandType Application -ErrorAction SilentlyContinue)) {
        throw "Native command is unavailable: $Path"
    }
    $previousErrorAction = $ErrorActionPreference
    $raw = @()
    $code = $null
    try {
        $ErrorActionPreference = "Continue"
        # Native processes update the global automatic variable in Windows
        # PowerShell 5.  Use that scope explicitly so a local assignment does not
        # shadow the exit code we are about to validate.
        $global:LASTEXITCODE = $null
        $raw = @(& $Path @Arguments 2>&1)
        $code = $global:LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
    }
    if ($null -eq $code) { throw "Native command did not return an exit code: $Path" }
    return [pscustomobject]@{ Output = @($raw); ExitCode = [int]$code }
}

function Request-InstallMaintenance {
    param([Parameter(Mandatory = $true)][string]$InstalledBinary,
          [Parameter(Mandatory = $true)][string]$StateDir,
          [bool]$AllowLegacy = $false)
    $capture = Invoke-NativeCaptured -Path $InstalledBinary `
        -Arguments @("maintenance", "prepare-install", "--json")
    $raw = @($capture.Output)
    $code = $capture.ExitCode
    $parsed = $false
    $value = $null
    try {
        $value = ($raw -join "`n") | ConvertFrom-Json
        $parsed = $true
    } catch {
        if ($code -eq 0) {
            throw "Running Agent returned an invalid install-maintenance response."
        }
    }
    if ($parsed) {
        $readyType = if ($null -eq $value.ready) { "" } else {
            $value.ready.GetType().FullName
        }
        $nonceType = if ($null -eq $value.nonce) { "" } else {
            $value.nonce.GetType().FullName
        }
        if ($readyType -ne "System.Boolean" -or $value.ready -ne $true) {
            throw "Running Agent did not grant install maintenance: $($value.status)"
        }
        if ($code -ne 0 -or $nonceType -ne "System.String" -or
            [string]$value.nonce -notmatch "^[0-9a-f]{32}$") {
            throw "Running Agent returned an invalid install-maintenance grant."
        }
        return [string]$value.nonce
    }
    if (-not $AllowLegacy) {
        throw "Running Agent lacks atomic install maintenance. Upgrade from this legacy build requires an explicitly supervised idle migration."
    }
    $markers = @(Get-ChildItem -LiteralPath $StateDir -Filter "paid-call-*.json" `
        -File -ErrorAction SilentlyContinue)
    $helpers = @(Get-Process -Name "mdd-call-audio-helper" -ErrorAction SilentlyContinue)
    if ($markers.Count -gt 0 -or $helpers.Count -gt 0) {
        throw "Legacy maintenance preflight found paid-call state or a live audio helper; installation is refused."
    }
    Write-Warning "Using one-time legacy idle migration; future upgrades use the atomic Agent maintenance gate."
    return ""
}

function Cancel-InstallMaintenance {
    param([Parameter(Mandatory = $true)][string]$InstalledBinary,
          [Parameter(Mandatory = $true)][string]$Nonce)
    if (-not $Nonce) { return }
    $capture = Invoke-NativeCaptured -Path $InstalledBinary `
        -Arguments @("maintenance", "cancel-install", "--nonce", $Nonce, "--json")
    $raw = @($capture.Output)
    if ($capture.ExitCode -ne 0) {
        throw "Could not cancel the Agent install-maintenance fence: $($raw -join ' ')"
    }
    try { $value = ($raw -join "`n") | ConvertFrom-Json } catch {
        throw "Agent returned an invalid maintenance-cancellation response."
    }
    $cancelledType = if ($null -eq $value.cancelled) { "" } else {
        $value.cancelled.GetType().FullName
    }
    if ($cancelledType -ne "System.Boolean" -or $value.cancelled -ne $true) {
        throw "Agent did not cancel the install-maintenance fence."
    }
}

function Get-LegacyTaskProfilePath {
    param([Parameter(Mandatory = $true)]$Task,
          [Parameter(Mandatory = $true)][string]$InstallerSid)
    $account = [string]$Task.Principal.UserId
    if (-not $account) { throw "The legacy Agent task has no principal." }
    $sid = (New-Object Security.Principal.NTAccount($account)).Translate(
        [Security.Principal.SecurityIdentifier]).Value
    if ($sid -ne $InstallerSid) {
        throw "The legacy Agent task belongs to a different account; explicit migration is required."
    }
    $escapedSid = $sid.Replace("'", "''")
    $profile = Get-CimInstance Win32_UserProfile -Filter "SID='$escapedSid'" `
        -ErrorAction Stop | Select-Object -First 1
    if (-not $profile.LocalPath) { throw "The legacy Agent task profile is unavailable." }
    return [string]$profile.LocalPath
}

function Assert-LegacyTaskMaintenance {
    param([Parameter(Mandatory = $true)]$Task,
          [Parameter(Mandatory = $true)][string]$InstallerSid,
          [bool]$AllowLegacy = $false)
    $profilePath = Get-LegacyTaskProfilePath -Task $Task -InstallerSid $InstallerSid
    $legacyStateDir = Join-Path $profilePath ".mdd-agent\state"
    $action = @($Task.Actions) | Select-Object -First 1
    $actionBinary = if ($action) {
        [Environment]::ExpandEnvironmentVariables([string]$action.Execute).Trim('"')
    } else { "" }
    if ($actionBinary -and [IO.Path]::GetFileName($actionBinary) -ieq "mdd-agent.exe" -and
        (Test-Path -LiteralPath $actionBinary -PathType Leaf)) {
        $nonce = Request-InstallMaintenance -InstalledBinary $actionBinary `
            -StateDir $legacyStateDir -AllowLegacy $AllowLegacy
        return [ordered]@{ binary = $actionBinary; nonce = $nonce }
    }
    if (-not $AllowLegacy) {
        throw "An enabled legacy Agent task requires an explicitly supervised idle migration."
    }
    $stateDirectories = @(
        $legacyStateDir,
        (Join-Path $profilePath "AppData\Roaming\MDD Agent\state")
    )
    $markers = @($stateDirectories | ForEach-Object {
        Get-ChildItem -LiteralPath $_ -Filter "paid-call-*.json" `
            -File -ErrorAction SilentlyContinue
    })
    $helpers = @(Get-Process -Name "mdd-call-audio-helper" -ErrorAction SilentlyContinue)
    if ($markers.Count -gt 0 -or $helpers.Count -gt 0) {
        throw "Legacy task maintenance found paid-call state or a live audio helper; installation is refused."
    }
    Write-Warning "Using one-time supervised migration for the legacy scheduled task."
    return [ordered]@{ binary = ""; nonce = "" }
}

function Import-LegacyAgentIdentity {
    param([Parameter(Mandatory = $true)]$Task,
          [Parameter(Mandatory = $true)][string]$TargetPath,
          [Parameter(Mandatory = $true)][string]$InstallerSid)

    # An existing ProgramData identity is authoritative.  Validate it and keep it; a
    # legacy user profile must never overwrite a normal upgrade or reinstall identity.
    if (Test-Path -LiteralPath $TargetPath -PathType Leaf) {
        $installed = Get-Content -LiteralPath $TargetPath -Raw | ConvertFrom-Json
        $installedId = [string]$installed.agent_id
        if ($installed.version -ne 1 -or $installedId -notmatch "^[0-9a-fA-F]{32}$") {
            throw "The installed Agent identity is invalid; refusing to overwrite it."
        }
        return $false
    }

    $profilePath = Get-LegacyTaskProfilePath -Task $Task -InstallerSid $InstallerSid
    $legacyPath = Join-Path $profilePath ".mdd-agent\identity.json"
    if (-not (Test-Path -LiteralPath $legacyPath -PathType Leaf)) { return $false }
    $legacy = Get-Content -LiteralPath $legacyPath -Raw | ConvertFrom-Json
    $agentId = [string]$legacy.agent_id
    if ($legacy.version -ne 1 -or $agentId -notmatch "^[0-9a-fA-F]{32}$") {
        throw "The legacy Agent identity is invalid; refusing to manufacture a replacement identity."
    }
    $temporary = "$TargetPath.migrating.$([Guid]::NewGuid().ToString('N'))"
    try {
        $document = [ordered]@{ version = 1; agent_id = $agentId.ToLowerInvariant() } |
            ConvertTo-Json
        [IO.File]::WriteAllText($temporary, $document,
            (New-Object Text.UTF8Encoding($false)))
        Move-Item -LiteralPath $temporary -Destination $TargetPath
    } finally {
        if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force }
    }
    return $true
}

Assert-Administrator

if ($Action -eq "Prepare") {
    New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $DataDir "logs") -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $DataDir "state") -Force | Out-Null
    Set-ProtectedAcl -Path $DataDir
    Write-Output '{"ok":true,"action":"prepare"}'
    exit 0
}

if ($Action -eq "Uninstall") {
    Stop-ServiceBounded -Name $serviceName
    Wait-AgentLeaseReleased
    Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("delete", $serviceName) `
        -AllowedExitCodes @(0, 1060)
    Wait-ServiceAbsent -Name $serviceName
    if (Test-Path -LiteralPath $installRoot) {
        Remove-Item -LiteralPath $installRoot -Recurse -Force
    }
    if ($PurgeData -and (Test-Path -LiteralPath $DataDir)) {
        Remove-Item -LiteralPath $DataDir -Recurse -Force
    }
    Write-Output '{"ok":true,"action":"uninstall"}'
    exit 0
}

if (-not (Test-Path -LiteralPath $BinaryPath -PathType Leaf)) {
    throw "Agent executable not found: $BinaryPath"
}
if (-not [StringComparer]::Ordinal.Equals(
        [IO.Path]::GetFileName($BinaryPath), "mdd-agent.exe")) {
    throw "BinaryPath must reference the packaged mdd-agent.exe."
}
Assert-NoReparseComponents -Path $sourceRoot
foreach ($file in $requiredPackageFiles) {
    if (-not (Test-Path -LiteralPath (Join-Path $sourceRoot $file) -PathType Leaf)) {
        throw "Required Agent package component is missing: $file"
    }
}
$manifestPath = Join-Path $sourceRoot "manifest.json"
$manifestItem = Get-Item -LiteralPath $manifestPath -Force
if (($manifestItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Package manifest must not be a reparse point."
}
try {
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
} catch {
    throw "Package manifest is not valid JSON."
}
if (-not (Test-ExactPropertySet $manifest @("version", "architecture", "files")) -or
    -not (Test-NativeJsonInteger $manifest.version) -or [long]$manifest.version -ne 1 -or
    $null -eq $manifest.architecture -or
    $manifest.architecture.GetType().FullName -ne "System.String" -or
    -not [StringComparer]::Ordinal.Equals(
        [string]$manifest.architecture, "windows-amd64") -or
    -not ($manifest.files -is [System.Array])) {
    throw "Unsupported or wrong-architecture Agent package manifest."
}
$manifestEntries = @($manifest.files)
if ($manifestEntries.Count -eq 0) { throw "Agent package manifest has no payload files." }
$manifestNames = New-Object 'Collections.Generic.HashSet[string]' `
    ([StringComparer]::Ordinal)
$allowedManifestNames = New-Object 'Collections.Generic.HashSet[string]' `
    ([StringComparer]::Ordinal)
foreach ($allowedName in $allowedManifestFiles) { [void]$allowedManifestNames.Add($allowedName) }
foreach ($entry in $manifestEntries) {
    if (-not (Test-ExactPropertySet $entry @("name", "size", "sha256"))) {
        throw "Manifest file entry has an invalid schema."
    }
    $nameType = if ($null -eq $entry.name) { "" } else {
        $entry.name.GetType().FullName
    }
    $hashType = if ($null -eq $entry.sha256) { "" } else {
        $entry.sha256.GetType().FullName
    }
    $name = [string]$entry.name
    if ($nameType -ne "System.String" -or
        -not [StringComparer]::Ordinal.Equals([IO.Path]::GetFileName($name), $name) -or
        -not $allowedManifestNames.Contains($name) -or -not $manifestNames.Add($name)) {
        throw "Manifest contains an invalid, duplicate, or unsupported component name."
    }
    if ($hashType -ne "System.String" -or
        [string]$entry.sha256 -notmatch "^[0-9a-fA-F]{64}$") {
        throw "Manifest contains an invalid SHA-256 value for $name."
    }
    if (-not (Test-NativeJsonInteger $entry.size) -or [long]$entry.size -lt 0) {
        throw "Manifest contains an invalid size for $name."
    }
    $candidate = Join-Path $sourceRoot $name
    if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
        throw "Manifest component is missing: $name"
    }
    $actual = (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne ([string]$entry.sha256).ToLowerInvariant()) {
        throw "Manifest hash mismatch for $name"
    }
    if ([long](Get-Item -LiteralPath $candidate).Length -ne [long]$entry.size) {
        throw "Manifest size mismatch for $name"
    }
}
foreach ($name in $requiredManifestFiles) {
    if (-not $manifestNames.Contains($name)) {
        throw "Required payload is not covered by the manifest: $name"
    }
}
$manifestDigest = (Get-FileHash -LiteralPath $manifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
$allowlistPath = Join-Path $sourceRoot "control-agent-allowlist.env"
$allowlistItem = Get-Item -LiteralPath $allowlistPath -Force
if (($allowlistItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Package trust anchor must not be a reparse point."
}
$allowlistValue = [IO.File]::ReadAllText($allowlistPath, [Text.Encoding]::UTF8)
$expectedAllowlist = "MDD_ALLOWED_AGENT_PACKAGE_DIGESTS=$manifestDigest`n"
if (-not [StringComparer]::Ordinal.Equals($allowlistValue, $expectedAllowlist)) {
    throw "Package trust anchor does not match the verified manifest digest."
}
$sourceNames = New-Object 'Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal)
foreach ($sourceItem in (Get-ChildItem -LiteralPath $sourceRoot -Force)) {
    if ($sourceItem.PSIsContainer -or
        ($sourceItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Agent package source must be flat and contain no reparse points: $($sourceItem.Name)"
    }
    if (-not $sourceNames.Add($sourceItem.Name)) {
        throw "Agent package source contains a duplicate component: $($sourceItem.Name)"
    }
}
$expectedSourceNames = New-Object 'Collections.Generic.HashSet[string]' `
    ([StringComparer]::Ordinal)
[void]$expectedSourceNames.Add("manifest.json")
[void]$expectedSourceNames.Add("control-agent-allowlist.env")
foreach ($name in $manifestNames) { [void]$expectedSourceNames.Add($name) }
if ($sourceNames.Count -ne $expectedSourceNames.Count) {
    throw "Agent package source set does not exactly match the signed manifest."
}
foreach ($name in $sourceNames) {
    if (-not $expectedSourceNames.Contains($name)) {
        throw "Agent package source contains an unsigned component: $name"
    }
}
Assert-CallAudioHelperProtocol -Path (Join-Path $sourceRoot "mdd-call-audio-helper.exe")

$paidCallMarkers = @(Get-ChildItem -LiteralPath (Join-Path $DataDir "state") `
    -Filter "paid-call-*.json" -File -ErrorAction SilentlyContinue)
if ($paidCallMarkers.Count -gt 0) {
    throw "A paid-call safety marker is present; start the current Agent and confirm the call is idle before installing."
}

$installerIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
$installerAccount = $installerIdentity.Name
$installerSid = $installerIdentity.User.Value
$operatorGroupName = "MDD Agent Operators"
$operatorGroup = Get-CimInstance Win32_Group -Filter `
    "LocalAccount=True AND Name='$operatorGroupName'" -ErrorAction Stop
if (-not $operatorGroup) {
    Invoke-NativeChecked -FilePath "net.exe" -Arguments @(
        "localgroup", $operatorGroupName, "/add",
        "/comment:May operate the local MDD Agent without installing it")
    $operatorGroup = Get-CimInstance Win32_Group -Filter `
        "LocalAccount=True AND Name='$operatorGroupName'" -ErrorAction Stop
    if (-not $operatorGroup) { throw "The local Agent operator group was not created." }
}
$operatorMembers = @(Get-CimAssociatedInstance -InputObject $operatorGroup `
    -Association Win32_GroupUser -ErrorAction Stop)
if (-not ($operatorMembers | Where-Object { $_.Caption -ieq $installerAccount })) {
    Invoke-NativeChecked -FilePath "net.exe" -Arguments @(
        "localgroup", $operatorGroupName, $installerAccount, "/add")
    $operatorMembers = @(Get-CimAssociatedInstance -InputObject $operatorGroup `
        -Association Win32_GroupUser -ErrorAction Stop)
    if (-not ($operatorMembers | Where-Object { $_.Caption -ieq $installerAccount })) {
        throw "The installing account was not added to the local Agent operator group."
    }
}
$operatorSid = (New-Object Security.Principal.NTAccount("$env:COMPUTERNAME\MDD Agent Operators")).Translate(
    [Security.Principal.SecurityIdentifier]).Value

New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $DataDir "logs") -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $DataDir "state") -Force | Out-Null
Set-ProtectedAcl -Path $DataDir

$stage = "$installRoot.staging.$([Guid]::NewGuid().ToString('N'))"
$backup = "$installRoot.backup.$([Guid]::NewGuid().ToString('N'))"
try {
    New-Item -ItemType Directory -Path $stage -Force | Out-Null
    Copy-Item -LiteralPath $BinaryPath -Destination (Join-Path $stage "mdd-agent.exe") -Force
    foreach ($file in $requiredFiles) {
        Copy-Item -LiteralPath (Join-Path $sourceRoot $file) `
            -Destination (Join-Path $stage $file) -Force
    }
    $optionalGammu = Join-Path $sourceRoot "gammu.exe"
    if ($manifestNames.Contains("gammu.exe")) {
        Copy-Item -LiteralPath $optionalGammu -Destination (Join-Path $stage "gammu.exe") -Force
    }
    Set-ProtectedAcl -Path $stage -AllowUsersRead $true
    foreach ($entry in $manifestEntries) {
        $stagedCandidate = Join-Path $stage $entry.name
        if (-not (Test-Path -LiteralPath $stagedCandidate -PathType Leaf)) {
            throw "Staged manifest component is missing: $($entry.name)"
        }
        $stagedHash = (Get-FileHash -LiteralPath $stagedCandidate -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($stagedHash -ne ([string]$entry.sha256).ToLowerInvariant()) {
            throw "Staged manifest hash mismatch for $($entry.name)"
        }
        if ([long](Get-Item -LiteralPath $stagedCandidate).Length -ne [long]$entry.size) {
            throw "Staged manifest size mismatch for $($entry.name)"
        }
    }
    $stagedNames = New-Object 'Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal)
    foreach ($stagedItem in (Get-ChildItem -LiteralPath $stage -Force)) {
        if ($stagedItem.PSIsContainer -or
            ($stagedItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
            -not $stagedNames.Add($stagedItem.Name)) {
            throw "Staged package must be flat, unique, and contain no reparse points."
        }
    }
    $expectedStageNames = New-Object 'Collections.Generic.HashSet[string]' `
        ([StringComparer]::Ordinal)
    [void]$expectedStageNames.Add("manifest.json")
    foreach ($name in $manifestNames) { [void]$expectedStageNames.Add($name) }
    if ($stagedNames.Count -ne $expectedStageNames.Count) {
        throw "Staged payload set does not exactly match the signed manifest."
    }
    foreach ($name in $stagedNames) {
        if (-not $expectedStageNames.Contains($name)) {
            throw "Staged package contains an unsigned component: $name"
        }
    }
    $stagedManifest = Join-Path $stage "manifest.json"
    $stagedManifestDigest = (Get-FileHash -LiteralPath $stagedManifest `
        -Algorithm SHA256).Hash.ToLowerInvariant()
    if (-not [StringComparer]::Ordinal.Equals($stagedManifestDigest, $manifestDigest)) {
        throw "Staged package manifest digest does not match the source package."
    }
    Assert-CallAudioHelperProtocol -Path (Join-Path $stage "mdd-call-audio-helper.exe")
} catch {
    $stageFailure = $_
    if (Test-Path -LiteralPath $stage) {
        try { Remove-Item -LiteralPath $stage -Recurse -Force } catch {
            Write-Warning "Could not remove failed staging directory $stage"
        }
    }
    throw $stageFailure
}

$serviceExisted = $null -ne (Get-Service -Name $serviceName -ErrorAction SilentlyContinue)
$legacyTask = Get-ScheduledTask -TaskName $legacyTaskName -ErrorAction SilentlyContinue
$legacyWasEnabled = if ($legacyTask) { [bool]$legacyTask.Settings.Enabled } else { $false }
$legacyWasRunning = if ($legacyTask) { [string]$legacyTask.State -eq "Running" } else { $false }
$oldInstallMoved = $false
$newInstallActivated = $false
$newServiceCreated = $false
$legacyIdentityCreated = $false
$installSucceeded = $false
$preserveRecoveryArtifacts = $false
$serviceBackupReg = ""
$oldServiceSddl = ""
$oldServiceWasRunning = $false
$serviceStopAttempted = $false
$maintenanceNonce = ""
$installedPreflightBinary = Join-Path $installRoot "mdd-agent.exe"
$legacyTaskStopAttempted = $false
$legacyMaintenanceNonce = ""
$legacyMaintenanceBinary = ""
if ($serviceExisted) {
    $oldServiceWasRunning = [string](Get-Service -Name $serviceName).Status -eq "Running"
    $serviceBackupReg = Join-Path $env:TEMP ("mdd-agent-service-{0}.reg" -f [Guid]::NewGuid().ToString("N"))
    Invoke-NativeChecked -FilePath "reg.exe" -Arguments @("export",
        "HKLM\SYSTEM\CurrentControlSet\Services\$serviceName", $serviceBackupReg, "/y")
    $sddlOutput = & sc.exe sdshow $serviceName
    if ($LASTEXITCODE -ne 0) { throw "Could not save existing MddAgent service DACL." }
    $oldServiceSddl = [string]($sddlOutput | Where-Object { $_ -match "^D:" } | Select-Object -First 1)
    if (-not $oldServiceSddl) { throw "Existing MddAgent service DACL was empty." }
}

try {
    if ($serviceExisted) {
        $maintenanceNonce = Request-InstallMaintenance `
            -InstalledBinary $installedPreflightBinary `
            -StateDir (Join-Path $DataDir "state") `
            -AllowLegacy ([bool]$AllowLegacyMaintenancePreflight)
    }
    if ($legacyTask) {
        if ($legacyWasEnabled -or $legacyWasRunning) {
            $legacyMaintenance = Assert-LegacyTaskMaintenance `
                -Task $legacyTask -InstallerSid $installerSid `
                -AllowLegacy ([bool]$AllowLegacyMaintenancePreflight)
            $legacyMaintenanceNonce = [string]$legacyMaintenance.nonce
            $legacyMaintenanceBinary = [string]$legacyMaintenance.binary
        }
        $legacyTaskStopAttempted = $true
        Stop-ScheduledTask -TaskName $legacyTaskName -ErrorAction SilentlyContinue
    }
    if ($serviceExisted) {
        $serviceStopAttempted = $true
        Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("stop", $serviceName) `
            -AllowedExitCodes @(0, 1062)
        Wait-ServiceState -Name $serviceName -State "Stopped"
    }
    # SCM can publish STOPPED before a PyInstaller one-file host and its Python child have
    # closed every process handle.  Starting the replacement during that small window makes
    # the new service fail its host-wide singleton check and can trigger an SCM restart loop.
    # Wait on the actual installation-scoped lease rather than guessing a sleep duration.
    Wait-AgentLeaseReleased

    # Moving the same installation from an interactive task to LocalSystem must not manufacture
    # a new host identity merely because the service has a different user profile.  This write is
    # part of the installation transaction and is removed on rollback.  Only the current elevated
    # installer account may authorize its own enabled legacy task; existing ProgramData always wins.
    if (-not $serviceExisted -and $legacyTask -and
        ($legacyWasEnabled -or $legacyWasRunning)) {
        $legacyIdentityCreated = Import-LegacyAgentIdentity -Task $legacyTask `
            -TargetPath (Join-Path $DataDir "state\identity.json") `
            -InstallerSid $installerSid
    }

    $conflicts = Get-CimInstance Win32_Process | Where-Object {
        ($_.Name -match "^mdd-(modem|card)-agent.*\.exe$") -or
        (($_.Name -match "^pythonw?\.exe$") -and
         $_.CommandLine -match "(modem_agent|card_agent)\.py")
    }
    if ($conflicts) {
        $ids = ($conflicts | Select-Object -ExpandProperty ProcessId) -join ","
        throw "A legacy foreground MDD Agent is still running (PID $ids). Stop it explicitly; the installer will not terminate it."
    }

    if (Test-Path -LiteralPath $installRoot) {
        Move-Item -LiteralPath $installRoot -Destination $backup
        $oldInstallMoved = $true
    }
    Move-Item -LiteralPath $stage -Destination $installRoot
    $newInstallActivated = $true
    Set-ProtectedAcl -Path $installRoot -AllowUsersRead $true
    $installedBinary = Join-Path $installRoot "mdd-agent.exe"
    $quotedCommand = ('"{0}" service run' -f $installedBinary)

    if ($serviceExisted) {
        $serviceInstance = Get-CimInstance Win32_Service -Filter "Name='$serviceName'" `
            -ErrorAction Stop
        $changeResult = Invoke-CimMethod -InputObject $serviceInstance -MethodName Change `
            -Arguments @{
                DisplayName = "MDD Unified Modem and Smart Card Agent"
                PathName = $quotedCommand
                StartMode = "Automatic"
            } -ErrorAction Stop
        if ([int]$changeResult.ReturnValue -ne 0) {
            throw "Win32_Service.Change failed with code $($changeResult.ReturnValue)."
        }
    } else {
        $createResult = Invoke-CimMethod -ClassName Win32_Service -MethodName Create `
            -Arguments @{
                Name = $serviceName
                DisplayName = "MDD Unified Modem and Smart Card Agent"
                PathName = $quotedCommand
                ServiceType = [byte]16
                ErrorControl = [byte]1
                StartMode = "Automatic"
                DesktopInteract = $false
                StartName = "LocalSystem"
            } -ErrorAction Stop
        if ([int]$createResult.ReturnValue -ne 0) {
            throw "Win32_Service.Create failed with code $($createResult.ReturnValue)."
        }
        $newServiceCreated = $true
    }
    Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("description", $serviceName,
        "Owns local 4G/5G modems and PC/SC readers for MDD.")
    Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("failure", $serviceName,
        "reset=", "86400", "actions=", "restart/5000/restart/15000/restart/60000")
    Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("failureflag", $serviceName, "1")
    Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("sidtype", $serviceName, "unrestricted")
    $serviceSddl = "D:(A;;CCLCSWLOCRRC;;;AU)(A;;CCLCSWRPWPDTLOCRRC;;;$operatorSid)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)"
    Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("sdset", $serviceName, $serviceSddl)
    New-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\$serviceName" `
        -Name Environment -PropertyType MultiString `
        -Value @("MDD_AGENT_DATA_DIR=$DataDir") -Force | Out-Null

    $imagePath = (Get-ItemProperty "HKLM:\SYSTEM\CurrentControlSet\Services\$serviceName").ImagePath
    if ($imagePath -notlike "*$installedBinary*") {
        throw "SCM ImagePath does not reference the protected installed binary: $imagePath"
    }
    Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("start", $serviceName)
    Wait-ServiceState -Name $serviceName -State "Running"
    Assert-CallAudioHelperProtocol -Path (Join-Path $installRoot "mdd-call-audio-helper.exe")

    $healthDeadline = (Get-Date).AddSeconds(45)
    $stableHealthSamples = 0
    do {
        $statusCapture = Invoke-NativeCaptured -Path $installedBinary `
            -Arguments @("status", "--json")
        $statusRaw = @($statusCapture.Output) -join "`n"
        $statusExit = $statusCapture.ExitCode
        $doctorCapture = Invoke-NativeCaptured -Path $installedBinary `
            -Arguments @("doctor", "--json")
        $doctorExit = $doctorCapture.ExitCode
        $selfTestCapture = Invoke-NativeCaptured -Path $installedBinary `
            -Arguments @("self-test", "--json")
        $selfTestExit = $selfTestCapture.ExitCode
        $runtimeState = ""
        $modemConnected = $false
        $runtimePackageDigest = ""
        if ($statusExit -eq 0) {
            try {
                $statusObject = $statusRaw | ConvertFrom-Json
                $runtimeState = $statusObject.runtime.runtime
                $modemConnected = [bool]$statusObject.runtime.modem.connected
                $runtimePackageDigest = [string]$statusObject.runtime.package_digest
            } catch {
                $runtimeState = ""
                $modemConnected = $false
                $runtimePackageDigest = ""
            }
        }
        $runtimeReady = if ($ReaderOnly) {
            $runtimeState -in @("ready", "online")
        } else {
            $runtimeState -eq "online" -and $modemConnected
        }
        $packageReady = [StringComparer]::Ordinal.Equals(
            $runtimePackageDigest, $manifestDigest)
        if ($statusExit -eq 0 -and $doctorExit -eq 0 -and $selfTestExit -eq 0 -and
            $runtimeReady -and $packageReady) {
            $stableHealthSamples++
            if ($stableHealthSamples -ge 2) { break }
        } else {
            $stableHealthSamples = 0
        }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $healthDeadline)
    if ($stableHealthSamples -lt 2) {
        throw "MDD Agent local control health check failed (status=$statusExit, doctor=$doctorExit, self_test=$selfTestExit, runtime=$runtimeState, modem_connected=$modemConnected, package_digest=$runtimePackageDigest, expected_package_digest=$manifestDigest, reader_only=$ReaderOnly)."
    }

    if ($legacyTask) { Disable-ScheduledTask -TaskName $legacyTaskName | Out-Null }
    if ($oldInstallMoved -and (Test-Path -LiteralPath $backup)) {
        Remove-Item -LiteralPath $backup -Recurse -Force
    }
    $installSucceeded = $true
} catch {
    $installFailure = $_
    if (-not $serviceStopAttempted -and -not $legacyTaskStopAttempted -and
        -not $oldInstallMoved -and
        -not $newInstallActivated -and -not $newServiceCreated) {
        if ($maintenanceNonce) {
            try {
                Cancel-InstallMaintenance -InstalledBinary $installedPreflightBinary `
                    -Nonce $maintenanceNonce
            } catch {
                Write-Warning $_.Exception.Message
            }
        }
        if ($legacyMaintenanceNonce -and $legacyMaintenanceBinary) {
            try {
                Cancel-InstallMaintenance -InstalledBinary $legacyMaintenanceBinary `
                    -Nonce $legacyMaintenanceNonce
            } catch {
                Write-Warning $_.Exception.Message
            }
        }
        throw $installFailure
    }
    $rollbackErrors = @()
    $rollbackSafeToMutate = $true
    try { Stop-ServiceBounded -Name $serviceName } catch {
        $rollbackSafeToMutate = $false
        $rollbackErrors += $_.Exception.Message
    }
    try { Wait-AgentLeaseReleased } catch {
        $rollbackSafeToMutate = $false
        $rollbackErrors += $_.Exception.Message
    }
    if (-not $rollbackSafeToMutate) {
        $preserveRecoveryArtifacts = $true
        foreach ($rollbackError in $rollbackErrors) { Write-Warning "Rollback: $rollbackError" }
        $recoveryDetail = if ($serviceBackupReg) {
            " Service recovery data was preserved at $serviceBackupReg."
        } else { "" }
        throw "Installation failed ($($installFailure.Exception.Message)). Rollback stopped safely before changing files or service registration because the previous device owner did not fully stop.$recoveryDetail"
    }
    if ($newServiceCreated) {
        try {
            Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("delete", $serviceName) `
                -AllowedExitCodes @(0, 1060)
            Wait-ServiceAbsent -Name $serviceName
        } catch { $rollbackErrors += $_.Exception.Message }
    }
    try {
        if ($newInstallActivated -and (Test-Path -LiteralPath $installRoot)) {
            Remove-Item -LiteralPath $installRoot -Recurse -Force
        }
    } catch { $rollbackErrors += $_.Exception.Message }
    try {
        if ($oldInstallMoved -and (Test-Path -LiteralPath $backup)) {
            Move-Item -LiteralPath $backup -Destination $installRoot
        }
    } catch { $rollbackErrors += $_.Exception.Message }
    try {
        if ($legacyIdentityCreated) {
            Remove-Item -LiteralPath (Join-Path $DataDir "state\identity.json") -Force
        }
    } catch { $rollbackErrors += $_.Exception.Message }
    if ($serviceExisted) {
        try {
            Invoke-NativeChecked -FilePath "reg.exe" -Arguments @("import", $serviceBackupReg)
            Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("sdset", $serviceName, $oldServiceSddl)
            if ($oldServiceWasRunning) {
                Invoke-NativeChecked -FilePath "sc.exe" -Arguments @("start", $serviceName) `
                    -AllowedExitCodes @(0, 1056)
            }
        } catch { $rollbackErrors += $_.Exception.Message }
    }
    try {
        if ($legacyTask) {
            if ($legacyWasEnabled -or $legacyWasRunning) {
                Enable-ScheduledTask -TaskName $legacyTaskName | Out-Null
            } else {
                Disable-ScheduledTask -TaskName $legacyTaskName | Out-Null
            }
            if ($legacyWasRunning) {
                Start-ScheduledTask -TaskName $legacyTaskName -ErrorAction Stop
                if (-not $legacyWasEnabled) {
                    Disable-ScheduledTask -TaskName $legacyTaskName | Out-Null
                }
            } elseif (-not $legacyWasEnabled) {
                Disable-ScheduledTask -TaskName $legacyTaskName | Out-Null
            }
        }
    } catch { $rollbackErrors += $_.Exception.Message }
    if ($rollbackErrors.Count -gt 0) { $preserveRecoveryArtifacts = $true }
    foreach ($rollbackError in $rollbackErrors) { Write-Warning "Rollback: $rollbackError" }
    throw $installFailure
} finally {
    if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
    if (-not $preserveRecoveryArtifacts -and $serviceBackupReg -and
        (Test-Path -LiteralPath $serviceBackupReg)) {
        Remove-Item -LiteralPath $serviceBackupReg -Force
    }
}

Write-Output ('{{"ok":true,"action":"install","service":"MddAgent","root":"{0}"}}' -f `
    ($installRoot -replace '\\', '\\'))
