# Installs TRACE on Windows in two steps. First the binary: downloads the latest release for this machine, checks its
# SHA-256, puts it in %LOCALAPPDATA%\trace (or $env:TRACE_BIN_DIR) and adds that folder to the user PATH. Then the
# integration for the agent named in $env:TRACE_HOST (claude-code or codex): `trace install -host <it>`. For codex,
# run it from the project folder. $env:TRACE_HOME picks the memory's folder (default ~\.trace).
#
#   $env:TRACE_HOST = 'claude-code'; irm https://raw.githubusercontent.com/diegolinan/trace/main/install.ps1 | iex
#
# An existing trace.exe that differs is replaced only with $env:TRACE_UPDATE = '1'.
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$traceHost = $env:TRACE_HOST
if ($traceHost -ne 'claude-code' -and $traceHost -ne 'codex') {
    throw "set TRACE_HOST to claude-code or codex first (`$env:TRACE_HOST = 'claude-code'); nothing downloaded"
}
$integration = @('install', '-host', $traceHost)
if ($traceHost -eq 'codex') {
    if ($env:TRACE_CODEX_MODE -ne 'hooks' -and $env:TRACE_CODEX_MODE -ne 'agents') {
        throw "for codex, set TRACE_CODEX_MODE to hooks (Codex hooks, run once you trust them) or agents (a request in AGENTS.md); nothing downloaded"
    }
    $integration += @('-mode', $env:TRACE_CODEX_MODE)
}
if ($env:TRACE_UPDATE -and $env:TRACE_UPDATE -ne '1') {
    throw "TRACE_UPDATE is '$($env:TRACE_UPDATE)'; it is '1' to replace an existing trace.exe, or unset; nothing downloaded"
}
$previous = '' # the replaced binary, kept until the integration completes

$repo = 'diegolinan/trace'
$binDir = if ($env:TRACE_BIN_DIR) { $env:TRACE_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'trace' }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$asset = "trace_windows_$arch.exe"
$base = "https://github.com/$repo/releases/latest/download"
$exe = Join-Path $binDir 'trace.exe'

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

    if (Test-Path $exe) {
        $have = (Get-FileHash -Algorithm SHA256 $exe).Hash.ToLower()
        if ($have -eq $got) {
            Write-Host "$exe is already this release"
        } elseif ($env:TRACE_UPDATE -ne '1') {
            throw "$exe exists and is another build; set `$env:TRACE_UPDATE = '1' to replace it; nothing changed"
        } else {
            $previous = "$exe.previous"
            Copy-Item -Force $exe $previous
            Move-Item -Force (Join-Path $tmp $asset) $exe # fails while a hook runs the old one: nothing is half-replaced
            Write-Host "replaced $exe (the previous build is kept as $previous)"
        }
    } else {
        New-Item -ItemType Directory -Force -Path $binDir | Out-Null
        Move-Item (Join-Path $tmp $asset) $exe
        Write-Host "installed $exe"
    }
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

# trace writes its progress to stderr. With the output redirected (`*> log.txt`), Windows PowerShell turns each such
# line into an error record, and 'Stop' would abort on the first one, so success is decided by the exit code alone.
$ErrorActionPreference = 'Continue'
& $exe @integration
$code = $LASTEXITCODE
$ErrorActionPreference = 'Stop'
if ($code -ne 0) {
    $back = if ($previous) { " The build it replaced is at $previous; to go back, close the agents that run it and copy it over $exe." } else { '' }
    throw "the binary is installed at $exe, but the $traceHost integration did not complete (exit $code); see the messages above.$back"
}
if ($previous) { Remove-Item -Force $previous -ErrorAction SilentlyContinue }
Write-Host "TRACE is installed for $traceHost"
