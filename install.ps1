# Installs gh-runners on Windows:
#   irm https://raw.githubusercontent.com/MehdiMamas/gh-runners/main/install.ps1 | iex
# Options (environment): GH_RUNNERS_VERSION=v0.1.0, GH_RUNNERS_BIN=<folder>
# To join a main host, set the code from Add a machine on its dashboard first:
#   $env:GH_RUNNERS_JOIN='<code>'; irm .../install.ps1 | iex
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repo = 'MehdiMamas/gh-runners'
$bin = if ($env:GH_RUNNERS_BIN) { $env:GH_RUNNERS_BIN } else { Join-Path $env:LOCALAPPDATA 'Programs\gh-runners' }
$version = if ($env:GH_RUNNERS_VERSION) { $env:GH_RUNNERS_VERSION } else { 'latest' }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$asset = "gh-runners-windows-$arch.exe"
$base = if ($version -eq 'latest') { "https://github.com/$repo/releases/latest/download" } else { "https://github.com/$repo/releases/download/$version" }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("gh-runners-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Force $tmp | Out-Null
try {
    Write-Host "Downloading $asset ($version) ..."
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset)
    Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match " $([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $line) { throw "checksums.txt has no entry for $asset" }
    $want = ($line -split ' ')[0]
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash
    if ($want -ne $got) { throw "checksum mismatch for $asset (want $want, got $got)" }

    New-Item -ItemType Directory -Force $bin | Out-Null
    $exe = Join-Path $bin 'gh-runners.exe'
    $wasRunning = $false
    if (Test-Path $exe) {
        $wasRunning = (& $exe service status 2>$null | Select-String 'running:\s*true') -ne $null
        # A running exe cannot be overwritten but can be renamed.
        $old = "$exe.old"
        Remove-Item $old -Force -ErrorAction SilentlyContinue
        Move-Item $exe $old -Force
    }
    Move-Item (Join-Path $tmp $asset) $exe -Force
    Write-Host "Installed $exe"
    & $exe --version

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $bin) {
        [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ';' + $bin).TrimStart(';'), 'User')
        $env:Path += ";$bin"
        Write-Host "Added $bin to your user PATH (open a new terminal to pick it up)."
    }
    if ($wasRunning) {
        Write-Host 'Restarting the background service to load the new version ...'
        if ($env:GH_RUNNERS_NOW) {
            & $exe service stop
            & $exe service start
        } else {
            # Waits for running jobs; GH_RUNNERS_NOW=1 restarts at once.
            & $exe service restart --when-idle
        }
    }
    # Best effort: a Start Menu entry that opens the dashboard.
    try { & $exe shortcut install | Out-Null } catch { }
    if ($env:GH_RUNNERS_JOIN) {
        # The code works once: drop it from this shell before using it.
        $code = $env:GH_RUNNERS_JOIN
        Remove-Item Env:GH_RUNNERS_JOIN
        # Pairs with the main host and starts the daemon at login.
        & $exe join $code
        if ($LASTEXITCODE -ne 0) { throw 'gh-runners join failed (see above)' }
        Write-Host 'Done: the main host manages this machine now.'
    }
    else {
        Write-Host 'Next: run gh-runners to open the dashboard in your browser (or find gh-runners in the Start Menu)'
    }
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
