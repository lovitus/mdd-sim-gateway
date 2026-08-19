param([switch]$LaunchQFlash)
$ErrorActionPreference = "Stop"
. "$PSScriptRoot\Ec20Upgrade.Common.ps1"
Assert-Administrator

$kitRoot = Split-Path $PSScriptRoot -Parent
Stop-MddModemAgent
$atPort = Get-QuectelComPort 'Quectel USB AT Port'
$dmPort = Get-QuectelComPort 'Quectel USB DM Port'
$responses = Invoke-Ec20At $atPort @("ATI", "AT+GMR", "AT+CGSN")
$imei = Get-Ec20Imei $responses
$backupRoot = Join-Path $kitRoot "Backups\$imei"
$backup = Get-LatestDeviceBackup $backupRoot
if ($backup -and -not (Test-Path (Join-Path $backup.FullName "$imei.xqcn"))) { $backup = $null }
if (-not $backup) { throw "No per-device XQCN backup exists for IMEI $imei. Run step 01 first." }

$manifestPath = Join-Path $backup.FullName "SHA256SUMS.txt"
$expectedLine = Get-Content $manifestPath | Where-Object { $_ -match "  $imei\.xqcn$" }
$expected = ($expectedLine -split '\s+')[0]
$actual = (Get-FileHash (Join-Path $backup.FullName "$imei.xqcn") -Algorithm SHA256).Hash.ToLowerInvariant()
if (-not $expected -or $actual -ne $expected) { throw "The XQCN backup checksum does not match." }

foreach ($interface in @(netsh mbn show interfaces | Select-String '^\s+Name\s+:\s+(.+)$')) {
    $name = $interface.Matches[0].Groups[1].Value.Trim()
    netsh mbn disconnect interface="$name" | Out-Null
}
$firehose = Join-Path $kitRoot "Firmware\R08A06\update\firehose\prog_nand_firehose_9x07.mbn"
$qflash = Join-Path $kitRoot "Tools\QFlash_V7.0\QFlash_V7.0.exe"
if (-not (Test-Path $firehose) -or -not (Test-Path $qflash)) { throw "QFlash or R08A06 firmware is missing." }

Write-Output "PREPARE_PASS"
Write-Output "IMEI=$imei"
Write-Output "BACKUP=$($backup.FullName)"
Write-Output "QFLASH_DM_PORT=$dmPort"
Write-Output "QFLASH_BAUD=460800"
Write-Output "LOAD_FILE=$firehose"
Write-Warning "In QFlash select the numeric part of $dmPort, not the AT/NMEA/Bluetooth port."
if ($LaunchQFlash) { Start-Process -FilePath $qflash -WorkingDirectory (Split-Path $qflash) -Verb RunAs }
