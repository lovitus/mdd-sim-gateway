param([switch]$ResumeLatest)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\Ec20Upgrade.Common.ps1"
Assert-Administrator

$kitRoot = Split-Path $PSScriptRoot -Parent
$qfenix = Join-Path $kitRoot "Tools\qfenix.exe"
if (-not (Test-Path $qfenix)) { throw "Missing Tools\qfenix.exe" }

Stop-MddModemAgent
$atPort = Get-QuectelComPort 'Quectel USB AT Port'
$dmPort = Get-QuectelComPort 'Quectel USB DM Port'
$commands = @(
    "ATI", "AT+GMR", "AT+CGSN", "AT+QCCID", "AT+CPIN?", "AT+CSQ",
    "AT+CREG?", "AT+CGREG?", "AT+CEREG?", "AT+COPS?", "AT+QNWINFO",
    'AT+QMBNCFG="AutoSel"', 'AT+QMBNCFG="List"', 'AT+QCFG="ims"',
    'AT+QCFG="volte_disable"', 'AT+QCFG="usbcfg"'
)
$responses = Invoke-Ec20At $atPort $commands
$imei = Get-Ec20Imei $responses
$deviceBackupRoot = Join-Path $kitRoot "Backups\$imei"
if ($ResumeLatest) {
    $backupDir = Get-ChildItem $deviceBackupRoot -Directory -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -match '^\d{8}-\d{6}$' } |
        Sort-Object Name -Descending | Select-Object -First 1 -ExpandProperty FullName
    if (-not $backupDir) { throw "There is no backup to resume for IMEI $imei." }
} else {
    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $backupDir = Join-Path $deviceBackupRoot $stamp
    New-Item -ItemType Directory -Path $backupDir -Force | Out-Null
    Write-AtInventory $responses (Join-Path $backupDir "inventory-before.txt")
}

$xqcn = Join-Path $backupDir "$imei.xqcn"
$tar = Join-Path $backupDir "$imei-quick-efs.tar"

function Invoke-QFenix([string[]]$arguments, [string]$logBase) {
    $stdout = "$logBase.stdout.log"
    $stderr = "$logBase.stderr.log"
    $process = Start-Process -FilePath $qfenix -ArgumentList $arguments -Wait -PassThru -NoNewWindow `
        -RedirectStandardOutput $stdout -RedirectStandardError $stderr
    return $process
}

if (-not ($ResumeLatest -and (Test-Path $xqcn) -and (Get-Item $xqcn).Length -ge 100KB)) {
    $process = Invoke-QFenix -arguments @("efsbackup", "-S", $dmPort, "-o", $xqcn) `
        -logBase (Join-Path $backupDir "qfenix-xqcn")
    if ($process.ExitCode -ne 0 -or -not (Test-Path $xqcn) -or (Get-Item $xqcn).Length -lt 100KB) {
        throw "XQCN backup failed or is implausibly small. Do not flash this device."
    }
}
if (Test-Path $tar) { [IO.File]::Delete($tar) }
$process = Invoke-QFenix -arguments @("efsbackup", "-S", $dmPort, "-t", "--quick", "-o", $tar) `
    -logBase (Join-Path $backupDir "qfenix-quick-efs")
if ($process.ExitCode -ne 0 -or -not (Test-Path $tar) -or (Get-Item $tar).Length -lt 4KB) {
    throw "Quick EFS backup failed or is implausibly small. Do not flash this device."
}

$manifest = foreach ($file in @($xqcn, $tar, (Join-Path $backupDir "inventory-before.txt"))) {
    $hash = Get-FileHash $file -Algorithm SHA256
    "$($hash.Hash.ToLowerInvariant())  $([IO.Path]::GetFileName($file))"
}
$manifest | Out-File (Join-Path $backupDir "SHA256SUMS.txt") -Encoding ascii
Restart-Ec20AfterDiagBackup $atPort
Write-Output "BACKUP_PASS"
Write-Output "IMEI=$imei"
Write-Output "AT_PORT=$atPort"
Write-Output "DM_PORT=$dmPort"
Write-Output "BACKUP=$backupDir"
Write-Warning "Keep this backup with this IMEI only. Never restore it to another module."
