$ErrorActionPreference = "Stop"
. "$PSScriptRoot\Ec20Upgrade.Common.ps1"
Assert-Administrator

$kitRoot = Split-Path $PSScriptRoot -Parent
$atPort = Get-QuectelComPort 'Quectel USB AT Port'
$commands = @(
    "ATI", "AT+GMR", "AT+CGSN", "AT+QCCID", "AT+CPIN?", "AT+CSQ",
    "AT+CREG?", "AT+CEREG?", "AT+COPS?", "AT+QNWINFO",
    'AT+QMBNCFG="AutoSel"', 'AT+QMBNCFG="List"', 'AT+QCFG="ims"',
    'AT+QCFG="volte_disable"'
)
$responses = Invoke-Ec20At $atPort $commands
$imei = Get-Ec20Imei $responses
$revision = [string]$responses["AT+GMR"]
if ($revision -notmatch 'EC20CEHDLGR08A06M1G') {
    throw "Unexpected firmware after flash: $revision"
}
$backupRoot = Join-Path $kitRoot "Backups\$imei"
$backup = Get-LatestDeviceBackup $backupRoot
if (-not $backup -or -not (Test-Path (Join-Path $backup.FullName "$imei.xqcn"))) {
    throw "Firmware is R08A06, but no matching pre-flash backup exists for IMEI $imei."
}
$afterPath = Join-Path $backup.FullName "inventory-after.txt"
Write-AtInventory $responses $afterPath
$afterHash = Get-FileHash $afterPath -Algorithm SHA256
"$($afterHash.Hash.ToLowerInvariant())  inventory-after.txt" |
    Add-Content (Join-Path $backup.FullName "SHA256SUMS.txt") -Encoding ascii

$before = Get-Content (Join-Path $backup.FullName "inventory-before.txt") -Raw
$beforeRevision = [regex]::Match($before, 'EC20CEHDLGR\w+').Value
$row = [pscustomobject]@{
    IMEI = $imei
    BeforeFirmware = $beforeRevision
    AfterFirmware = "EC20CEHDLGR08A06M1G"
    BackupDirectory = $backup.FullName
    VerifiedAt = (Get-Date).ToString("s")
    Result = "PASS"
}
$fleetLog = Join-Path $kitRoot "Fleet-Upgrade-Log.csv"
if (Test-Path $fleetLog) { $row | Export-Csv $fleetLog -NoTypeInformation -Append }
else { $row | Export-Csv $fleetLog -NoTypeInformation }
Write-Output "VERIFY_PASS"
Write-Output "IMEI=$imei"
Write-Output "FIRMWARE=EC20CEHDLGR08A06M1G"
Write-Output "EVIDENCE=$afterPath"
