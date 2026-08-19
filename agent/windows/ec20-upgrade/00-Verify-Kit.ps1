$ErrorActionPreference = "Stop"
$kitRoot = Split-Path $PSScriptRoot -Parent
$manifest = Join-Path $kitRoot "SHA256SUMS.txt"
if (-not (Test-Path $manifest)) { throw "Missing SHA256SUMS.txt" }
foreach ($line in Get-Content $manifest) {
    if (-not $line.Trim()) { continue }
    $match = [regex]::Match($line, '^([0-9a-fA-F]{64})\s{2}(.+)$')
    if (-not $match.Success) { throw "Invalid checksum line: $line" }
    $path = Join-Path $kitRoot $match.Groups[2].Value
    if (-not (Test-Path $path)) { throw "Missing kit file: $path" }
    $actual = (Get-FileHash $path -Algorithm SHA256).Hash
    if ($actual -ne $match.Groups[1].Value) { throw "Checksum mismatch: $path" }
}
$qflash = Join-Path $kitRoot "Tools\QFlash_V7.0\QFlash_V7.0.exe"
$signature = Get-AuthenticodeSignature $qflash
if ($signature.Status -ne "Valid" -or
        $signature.SignerCertificate.Subject -notmatch "Quectel Wireless Solutions") {
    throw "QFlash code signature is not valid Quectel software."
}
Write-Output "KIT_VERIFY_PASS"
