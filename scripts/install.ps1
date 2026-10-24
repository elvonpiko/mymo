# install.ps1 installs mymo on Windows from its official releases.
#
#   irm https://raw.githubusercontent.com/elvonpiko/mymo/main/scripts/install.ps1 | iex
#
# What it does, in order, and says each step out loud:
#   1. detects the machine's architecture
#   2. downloads the versioned release, showing its progress
#   3. verifies the archive against the published checksum — a
#      mismatch refuses the install, it never continues
#   4. installs to %LOCALAPPDATA%\Programs\mymo and puts that
#      directory on the user PATH
#   5. preserves the previous binary as mymo.old.exe — a bad
#      upgrade has a way back
#
# The face is mymo's own: statuses in caps, a progress bar built
# from the same stem block the TUI's mark is made of, mymo's
# palette. Color only when the console can take it — the legacy
# console ships with ANSI off, so it is switched on first, and
# anything that refuses stays plain, because a first touch must
# never print garbage.
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
$OnWindows = ($env:OS -eq "Windows_NT")

# the palette, and the escape that carries it. The legacy console
# ships with ANSI processing off — a Windows PowerShell 5.1
# console would print the escapes as garbage — so it is switched
# on through the console's own handle first, and anything that
# refuses, or a redirected output, keeps the words plain.
$Script:Esc = ""
try { $redirected = [Console]::IsOutputRedirected } catch { $redirected = $true }
if (-not $redirected) {
    if ($OnWindows) {
        try {
            if (-not ("mymoNativeConsole" -as [type])) {
                Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class mymoNativeConsole {
    [DllImport("kernel32.dll")] public static extern IntPtr GetStdHandle(int n);
    [DllImport("kernel32.dll")] public static extern bool GetConsoleMode(IntPtr h, out int m);
    [DllImport("kernel32.dll")] public static extern bool SetConsoleMode(IntPtr h, int m);
}
"@
            }
            $handle = [mymoNativeConsole]::GetStdHandle(-11)
            $mode = 0
            # 4 = ENABLE_VIRTUAL_TERMINAL_PROCESSING
            if ([mymoNativeConsole]::GetConsoleMode($handle, [ref]$mode) -and
                [mymoNativeConsole]::SetConsoleMode($handle, ($mode -bor 4))) {
                $Script:Esc = [string][char]27
            }
        } catch { }
    } else {
        $Script:Esc = [string][char]27
    }
}

$Mauve = "203;166;247"
$Blue = "180;190;254"
$Teal = "148;226;213"
$Green = "166;227;161"
$Sub = "166;173;200"

function Col($rgb, $s) {
    $e = $Script:Esc
    if ($e) { "$e[38;2;${rgb}m$s$e[0m" } else { "$s" }
}

# step prints a status line: the status in caps, the truth of the
# step in plain English beside it
function Step($status, $rest) {
    Write-Host ("{0}  {1}" -f (Col $Blue ($status.PadRight(8))), $rest)
}

function Fail($message) {
    # failures stay plain: they travel inside bug reports, and
    # escapes would garble them there
    throw "mymo install: $message"
}

# 1 - the architecture, layered. The runtime's own report first;
# the operating system's own environment variable beneath it.
# Windows PowerShell 5.1 - what `irm ... | iex` runs in by default -
# may not carry the .NET type at all, and PowerShell on another
# operating system may not carry the variable. One of the two
# always answers on a real Windows machine.
$archName = ""
try {
    $archName = [string][System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
} catch {
    $archName = ""
}
if ($archName -eq "" -or $archName -eq "Unknown") {
    $archName = $env:PROCESSOR_ARCHITECTURE
    if ($archName -eq "x86" -and $env:PROCESSOR_ARCHITEW6432) {
        # a 32-bit shell on a 64-bit Windows reports the host
        $archName = $env:PROCESSOR_ARCHITEW6432
    }
}
# The switch matches case-insensitively: "Arm64" covers the
# operating system's "ARM64" spelling on its own, and a duplicate
# case would fire too — $Arch an array, the download URL wrong.
$Arch = switch ("$archName") {
    "X64"   { "amd64" }   # the .NET runtime's report
    "AMD64" { "amd64" }   # the operating system's spelling
    "Arm64" { "arm64" }   # both spellings, by case-insensitivity
    default { Fail "unsupported architecture '$archName' — mymo builds amd64 and arm64" }
}

Write-Host "$(Col $Mauve "mymo") $(Col $Sub $Version)"
Write-Host ""
Step DETECT "windows/$Arch"

# 2 - the release and its checksums
$Archive = "mymo-$Version-windows-$Arch.zip"
$Tmp = New-Item -ItemType Directory -Force -Path (Join-Path ([System.IO.Path]::GetTempPath()) "mymo-install-$([guid]::NewGuid())")

Add-Type -AssemblyName System.Net.Http
$client = New-Object System.Net.Http.HttpClient
# thirty minutes, not the hundred-second default: a slow link
# must be allowed to be slow, the bar shows the truth of it
$client.Timeout = [TimeSpan]::FromMinutes(30)

function Get-Response($url) {
    try {
        # ResponseHeadersRead: hand back the stream the moment the
        # headers arrive, or the whole body would buffer first and
        # the bar would jump from nothing to done
        return $client.GetAsync($url, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
    } catch [System.Net.Http.HttpRequestException] {
        # the network never got an answer: the path question
        Fail "download failed — the network path to github's release servers is not answering; a retry or a VPN often helps"
    } catch {
        Fail "download failed — $($_.Exception.Message)"
    }
}

function HSize([long]$bytes) {
    [math]::Round($bytes / 1MB, 1).ToString([System.Globalization.CultureInfo]::InvariantCulture)
}

function BarLine([long]$done, $total) {
    if ($total -and $total -gt 0) {
        $pct = [int][math]::Floor($done * 100 / $total)
        if ($pct -gt 100) { $pct = 100 }
        $filled = [int][math]::Floor(($done * 10 + $total - 1) / $total)
        if ($filled -gt 10) { $filled = 10 }
        $bar = ("█" * $filled) + ("░" * (10 - $filled))
        "$(Col $Teal $bar)  $pct% · $(HSize $done)/$(HSize $total) MiB"
    } else {
        # no size to measure against: bytes so far, no lie about
        # how far along
        "$(HSize $done) MiB so far"
    }
}

function Fetch-Sums($dest) {
    $r = Get-Response "$BaseUrl/SHA256SUMS"
    if (-not $r.IsSuccessStatusCode) {
        Fail "download failed — the server answered $($r.StatusCode) for SHA256SUMS"
    }
    $in = $r.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
    $out = [System.IO.File]::Create($dest)
    $in.CopyTo($out)
    $out.Close()
    $in.Close()
    $r.Dispose()
}

$archivePath = Join-Path $Tmp $Archive
$Script:Done = 0
# the response and its honesty are settled first, so the guard
# below cannot re-wrap a failure that already said its piece
$resp = Get-Response "$BaseUrl/$Archive"
if (-not $resp.IsSuccessStatusCode) {
    if ($resp.StatusCode -eq [System.Net.HttpStatusCode]::NotFound) {
        # an HTTP error answered: the release question
        Fail "download failed — is v$Version published?"
    }
    Fail "download failed — the server answered $($resp.StatusCode)"
}
$total = $resp.Content.Headers.ContentLength
$in = $resp.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
$out = [System.IO.File]::Create($archivePath)
$buf = New-Object byte[] 16384
try {
    # the bar redraws in place while the download moves; a stalled
    # link shows as a stalled bar, which is the truth of it
    while (($n = $in.Read($buf, 0, $buf.Length)) -gt 0) {
        $out.Write($buf, 0, $n)
        $Script:Done += $n
        [Console]::Write("`r$(Col $Blue "DOWNLOAD")  $(BarLine $Script:Done $total)")
    }
} catch {
    Fail "download failed — the transfer stopped: $($_.Exception.Message)"
}
$out.Close()
$in.Close()
$resp.Dispose()
[Console]::Write("`r$(Col $Blue "DOWNLOAD")  $(BarLine $Script:Done $total)")
Write-Host ""

# 3 - verify before anything touches the directory the binary
# will live in. Only this archive's line is used.
Fetch-Sums (Join-Path $Tmp "SHA256SUMS")
$sums = Get-Content (Join-Path $Tmp "SHA256SUMS")
$want = $null
foreach ($line in $sums) {
    $parts = $line -split '\s+', 2
    if ($parts.Count -eq 2 -and $parts[1].Trim() -eq $Archive) {
        $want = $parts[0]
    }
}
if (-not $want) {
    Fail "SHA256SUMS does not name $Archive — the release is not what the installer expected"
}
$sha = [System.Security.Cryptography.SHA256]::Create()
$stream = [System.IO.File]::OpenRead($archivePath)
$got = ([System.BitConverter]::ToString($sha.ComputeHash($stream)) -replace '-', '').ToLower()
$stream.Close()
if ($got -ne $want) {
    Fail "checksum mismatch — the archive is not what was published; nothing was installed"
}
Step VERIFY "checksum matches SHA256SUMS"

# 4 - where the binary lands
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

# 5 - preserve the previous version before replacing it
$dest = Join-Path $InstallDir "mymo.exe"
if (Test-Path $dest) {
    Copy-Item $dest (Join-Path $InstallDir "mymo.old.exe") -Force
    Step PRESERVE "the previous binary is kept as mymo.old.exe"
}

Expand-Archive -Path $archivePath -DestinationPath $Tmp -Force
Copy-Item (Join-Path $Tmp "mymo-$Version-windows-$Arch\mymo.exe") $dest -Force

if ($OnWindows) {
    # the new binary proves it runs before the installer claims
    # anything
    Step INSTALL "$dest ($(& $dest version))"
} else {
    # a PE binary cannot run here — this ceremony runs on Windows
    Step INSTALL $dest
}

# the PATH, for this user, idempotently
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallDir", "User")
    Write-Host ""
    Write-Host (Col $Sub "note: $InstallDir is now on your PATH — a new terminal sees it")
}

Remove-Item $Tmp -Recurse -Force
Write-Host ""
Write-Host (Col $Green "mymo is installed — run 'mymo' to open your fleet")
