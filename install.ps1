# Installs TRACE on Windows: downloads the latest release for this machine, checks its SHA-256, puts it in
# %LOCALAPPDATA%\trace (or $env:TRACE_BIN_DIR), adds that folder to the user PATH, then runs `trace install`, which
# adds the hooks to Claude Code's user settings and does the first import.
#
#   irm https://raw.githubusercontent.com/diegolinan/trace/main/install.ps1 | iex
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$repo = 'diegolinan/trace'
$binDir = if ($env:TRACE_BIN_DIR) { $env:TRACE_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'trace' }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$asset = "trace_windows_$arch.exe"
$base = "https://github.com/$repo/releases/latest/download"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("trace-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "downloading $asset..."
    Invoke-WebRequest -UseBasicParsing "$base/$asset" -OutFile (Join-Path $tmp $asset)
    Invoke-WebRequest -UseBasicParsing "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match " $([regex]::Escape($asset))$" }
    $want = if ($line) { ($line -split ' ')[0] } else { '' }
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash.ToLower()
    if (-not $want -or $want -ne $got) { throw "checksum mismatch for $asset, not installed" }

    New-Item -ItemType Directory -Force -Path $binDir | Out-Null
    $exe = Join-Path $binDir 'trace.exe'
    Move-Item -Force (Join-Path $tmp $asset) $exe
    Write-Host "installed $exe"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($null -eq $userPath) { $userPath = '' }
if ($env:TRACE_NO_PATH) {
    Write-Host "PATH left as it is (TRACE_NO_PATH)"
} elseif (-not (($userPath -split ';') -contains $binDir)) {
    [Environment]::SetEnvironmentVariable('Path', (($userPath.TrimEnd(';') + ';' + $binDir).TrimStart(';')), 'User')
    Write-Host "added $binDir to your user PATH (new terminals will see it)"
}

& $exe install
