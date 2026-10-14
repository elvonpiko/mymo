# install.ps1 installs mymo on Windows from its official releases.
#
#   irm https://raw.githubusercontent.com/elvonpiko/mymo/main/scripts/install.ps1 | iex
#
# What it does, in order, and says each step out loud:
#   1. detects the machine's architecture
#   2. downloads the versioned release and its checksums
#   3. verifies the archive against the published checksum — a
#      mismatch refuses the install, it never continues
#   4. installs to %LOCALAPPDATA%\Programs\mymo and puts that
#      directory on the user PATH
#   5. preserves the previous binary as mymo.old.exe — a bad
#      upgrade has a way back
#
# Overrides, for testing and mirrors:
#   $env:VERSION = "0.1.0"          which release to install
#   $env:BASE_URL = "<url>"         where releases live
#   $env:MYMO_INSTALL_DIR = "<dir>" where the binary lands
#
# mymo needs the OpenSSH client on PATH for its escape hatches
# ('mymo node ssh', 'mymo app shell'); the transport itself is
# built in. Windows 10 and 11 ship the OpenSSH client as an
# optional feature.

$ErrorActionPreference = "Stop"

$Version = if ($env:VERSION) { $env:VERSION } else { "0.1.0" }
$BaseUrl = if ($env:BASE_URL) { $env:BASE_URL } else { "https://github.com/elvonpiko/mymo/releases/download/v$Version" }
$InstallDir = if ($env:MYMO_INSTALL_DIR) { $env:MYMO_INSTALL_DIR } else {
    Join-Path $env:LOCALAPPDATA "Programs\mymo"
}

# 1 - the architecture, from the processor's own report
$Arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "mymo install: unsupported architecture '$($env:PROCESSOR_ARCHITECTURE)' - mymo builds amd64 and arm64" }
}

Write-Host "mymo install: $Version for windows/$Arch"

# 2 - the release and its checksums
$Archive = "mymo-$Version-windows-$Arch.zip"
$Tmp = New-Item -ItemType Directory -Force -Path (Join-Path $env:TEMP "mymo-install-$([guid]::NewGuid())")

$web = New-Object System.Net.WebClient
try {
    Write-Host "downloading $Archive"
    $web.DownloadFile("$BaseUrl/$Archive", (Join-Path $Tmp $Archive))
    $web.DownloadFile("$BaseUrl/SHA256SUMS", (Join-Path $Tmp "SHA256SUMS"))
} catch {
    throw "mymo install: download failed - is v$Version published?"
}

# 3 - verify before anything touches the directory the binary
# will live in. Only this archive's line is used.
$sums = Get-Content (Join-Path $Tmp "SHA256SUMS")
$want = $null
foreach ($line in $sums) {
    $parts = $line -split '\s+', 2
    if ($parts.Count -eq 2 -and $parts[1].Trim() -eq $Archive) {
        $want = $parts[0]
    }
}
if (-not $want) {
    throw "mymo install: SHA256SUMS does not name $Archive - the release is not what the installer expected"
}
$sha = [System.Security.Cryptography.SHA256]::Create()
$stream = [System.IO.File]::OpenRead((Join-Path $Tmp $Archive))
$got = ([System.BitConverter]::ToString($sha.ComputeHash($stream)) -replace '-', '').ToLower()
$stream.Close()
if ($got -ne $want) {
    throw "mymo install: checksum mismatch - the archive is not what was published; nothing was installed"
}
Write-Host "checksum verified"

# 4 - where the binary lands
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

# 5 - preserve the previous version before replacing it
$dest = Join-Path $InstallDir "mymo.exe"
if (Test-Path $dest) {
    Copy-Item $dest (Join-Path $InstallDir "mymo.old.exe") -Force
    Write-Host "previous binary preserved as mymo.old.exe"
}

Expand-Archive -Path (Join-Path $Tmp $Archive) -DestinationPath $Tmp -Force
Copy-Item (Join-Path $Tmp "mymo-$Version-windows-$Arch\mymo.exe") $dest -Force

# the PATH, for this user, idempotently
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallDir", "User")
    Write-Host "note: added $InstallDir to your PATH - open a new terminal to use 'mymo'"
}

Remove-Item $Tmp -Recurse -Force
Write-Host "installed: $dest"
