<#
.SYNOPSIS
  Install prerequisites and build Signal Station on Windows.

.DESCRIPTION
  Installs Go and a C toolchain (mingw-w64 via MSYS2) if they are missing, then
  builds Signal Station.exe.

  This script CANNOT build the macOS app. That requires Apple's SDK, which their
  licence restricts to Apple hardware, so no amount of tooling on Windows will
  produce one. Use a Mac with build.sh, or push the repo and run the GitHub
  Actions workflow in .github/workflows/build.yml, which builds both on hosted
  runners and gives you the results as downloadable artifacts.

.EXAMPLE
  .\build.ps1
  .\build.ps1 -SkipDeps
  .\build.ps1 -Mac          # explains the options for getting a macOS build
  .\build.ps1 -CC "C:\mingw64\bin"   # use a compiler you already have
#>

[CmdletBinding()]
param(
    [switch]$SkipDeps,
    [switch]$Clean,
    [switch]$Mac,
    # Directory containing gcc.exe, when you already have a toolchain and would
    # rather this script did not go looking for one.
    [string]$CC
)

$ErrorActionPreference = 'Stop'

$AppName    = 'Signal Station'
$AppId      = 'org.signalstation.app'
$AppVersion = '1.0.0'
$BinName    = 'signal-station'

Set-Location -Path $PSScriptRoot
$Dist = Join-Path $PSScriptRoot 'dist'

function Step($m) { Write-Host "`n==> $m" -ForegroundColor Cyan }
function Info($m) { Write-Host "    $m" }
function Warn($m) { Write-Host " !! $m" -ForegroundColor Yellow }
function Die($m)  { Write-Host " !! $m" -ForegroundColor Red; exit 1 }
function Have($n) { $null -ne (Get-Command $n -ErrorAction SilentlyContinue) }

# Find-Gcc searches every place a Windows GCC realistically lands and puts the
# first hit on PATH.
#
# MSYS2 ships several parallel toolchains and installing one does NOT put it on
# PATH. Modern MSYS2 defaults to the UCRT environment (ucrt64\bin, package
# mingw-w64-ucrt-x86_64-gcc), not the older mingw64. Assuming mingw64\bin is the
# single most common reason this step fails.
function Find-Gcc {
    $roots = @()
    foreach ($r in @('C:\msys64', 'C:\msys2', "$env:SystemDrive\msys64", 'C:\tools\msys64')) {
        if (Test-Path $r) { $roots += $r }
    }

    $candidates = @()
    foreach ($r in $roots) {
        $candidates += @(
            "$r\ucrt64\bin",     # MSYS2 default since 2022
            "$r\mingw64\bin",    # older MSVCRT environment
            "$r\clang64\bin"
        )
    }
    $candidates += @(
        'C:\mingw64\bin',                    # WinLibs / standalone extraction
        'C:\TDM-GCC-64\bin',
        'C:\MinGW\bin',
        'C:\ProgramData\chocolatey\bin',
        "$env:ProgramFiles\mingw64\bin",
        "$env:LOCALAPPDATA\Programs\mingw64\bin"
    )

    foreach ($c in $candidates) {
        if ($c -and (Test-Path (Join-Path $c 'gcc.exe'))) {
            $env:Path = "$c;$env:Path"
            # Pin CC to the absolute path. cgo reads CC directly, so this holds
            # even if PATH resolves differently in the child process.
            $env:CC  = (Join-Path $c 'gcc.exe')
            $env:CXX = if (Test-Path (Join-Path $c 'g++.exe')) { Join-Path $c 'g++.exe' } else { $null }
            Info "Found gcc in $c"
            return $true
        }
    }

    # Last resort: gcc is already on PATH from somewhere not in the list above.
    $onPath = Get-Command gcc -ErrorAction SilentlyContinue
    if ($onPath) {
        $env:CC = $onPath.Source
        Info "Found gcc on PATH: $($onPath.Source)"
        return $true
    }
    return $false
}

# Find-Go locates an existing Go install that is not on PATH, which happens when
# winget updates the machine PATH but the running shell keeps its old copy.
function Find-Go {
    if (Have 'go') { return $true }
    foreach ($p in @(
        "$env:ProgramFiles\Go\bin",
        "${env:ProgramFiles(x86)}\Go\bin",
        "$env:LOCALAPPDATA\Programs\Go\bin",
        'C:\Go\bin'
    )) {
        if ($p -and (Test-Path (Join-Path $p 'go.exe'))) {
            $env:Path = "$p;$env:Path"
            Info "Found go in $p"
            return $true
        }
    }
    return $false
}

# Initialize-Gcc locates a compiler, installing MSYS2 and a GCC package if none
# is present.
function Initialize-Gcc {
    if ($CC) {
        if (Test-Path (Join-Path $CC 'gcc.exe')) {
            $env:Path = "$CC;$env:Path"
            $env:CC   = (Join-Path $CC 'gcc.exe')
            $env:CXX  = if (Test-Path (Join-Path $CC 'g++.exe')) { Join-Path $CC 'g++.exe' } else { $null }
            Info "Using the compiler you specified: $env:CC"
            return
        }
        Die "No gcc.exe found in -CC path: $CC"
    }

    if (Find-Gcc) { return }
    if ($SkipDeps) { return }

    Step 'Installing a C compiler (Fyne needs one for cgo)'

    $msysRoot = @('C:\msys64', 'C:\msys2', 'C:\tools\msys64') |
                Where-Object { Test-Path (Join-Path $_ 'usr\bin\bash.exe') } |
                Select-Object -First 1

    if (-not $msysRoot) {
        Install-WingetPackage -Id 'MSYS2.MSYS2' -Label 'MSYS2 (a few minutes)' | Out-Null
        Update-PathFromRegistry
        $msysRoot = @('C:\msys64', 'C:\msys2', 'C:\tools\msys64') |
                    Where-Object { Test-Path (Join-Path $_ 'usr\bin\bash.exe') } |
                    Select-Object -First 1
    }

    if (-not $msysRoot) {
        Warn 'MSYS2 is not installed and winget could not install it.'
        return
    }
    Info "MSYS2 root: $msysRoot"

    $bash = Join-Path $msysRoot 'usr\bin\bash.exe'

    # A fresh MSYS2 needs its keyring and core packages synced before anything
    # else will install. The first -Syu can also close the shell partway through
    # a core update, which is why upstream tells you to run it twice.
    Info 'Syncing MSYS2 packages (first run of two)…'
    & $bash -lc 'pacman -Syu --noconfirm' 2>&1 | Out-Null
    Info 'Syncing MSYS2 packages (second run)…'
    & $bash -lc 'pacman -Syu --noconfirm' 2>&1 | Out-Null

    # Try the UCRT toolchain first, since that is the current default, then fall
    # back to the older one.
    foreach ($pkg in @('mingw-w64-ucrt-x86_64-gcc', 'mingw-w64-x86_64-gcc')) {
        Info "Installing $pkg…"
        & $bash -lc "pacman -S --noconfirm --needed $pkg"
        if ($LASTEXITCODE -eq 0 -and (Find-Gcc)) { return }
        Warn "$pkg did not yield a usable gcc; trying the next option."
    }

    Find-Gcc | Out-Null
}


#
# Install-WingetPackage pins --source winget deliberately.
#
# Without it, winget resolves against every configured source and will either
# prompt to disambiguate or pick msstore, which fails outright on machines where
# the Microsoft Store is disabled by policy. The winget community repository
# carries everything this script needs, so pinning costs nothing.
function Install-WingetPackage {
    param(
        [Parameter(Mandatory)][string]$Id,
        [Parameter(Mandatory)][string]$Label
    )

    Step "Installing $Label"
    winget install --id $Id --exact --source winget --silent `
        --accept-package-agreements --accept-source-agreements
    $code = $LASTEXITCODE

    # winget signals "already installed" and "no upgrade available" with
    # non-zero codes that are not failures. 0x8A150061 / 0x8A15002B unsigned.
    $benign = @(0, -1978335189, -1978335135, -1978335216)
    if ($benign -notcontains $code) {
        Warn "winget exited with code $code while installing $Label."
        Info "If this machine blocks the Microsoft Store, confirm the winget source exists:"
        Info '    winget source list'
        Info 'and if it is missing, restore it with:'
        Info '    winget source add --name winget --arg https://cdn.winget.microsoft.com/cache --type Microsoft.PreIndexed.Package'
        return $false
    }
    return $true
}

# winget writes to the machine PATH, but the running process keeps the PATH it
# started with. Re-read it so a freshly installed tool is usable immediately
# instead of demanding a new terminal.
function Update-PathFromRegistry {
    $machine = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    $user    = [Environment]::GetEnvironmentVariable('Path', 'User')
    $env:Path = (@($machine, $user) | Where-Object { $_ }) -join ';'
}

if ($Mac) {
    Step 'Building the macOS app'
    Warn 'A macOS build cannot be produced on Windows.'
    Info 'Fyne links against Apple''s frameworks through cgo, and Apple''s SDK'
    Info 'licence restricts that SDK to Apple hardware. Two ways to get one:'
    Info ''
    Info '  1. On a Mac:  ./build.sh --mac'
    Info '  2. On CI:     push this repo, then run the "Build executables"'
    Info '                workflow. It builds macOS and Windows and attaches'
    Info '                both as artifacts.'
    if (Have 'gh') {
        Info ''
        Info 'The GitHub CLI is installed, so you can start that now with:'
        Info '    gh workflow run "Build executables"'
    }
    exit 0
}

if ($Clean) {
    Step 'Cleaning'
    Remove-Item -Recurse -Force $Dist, "$AppName.exe", "$BinName.exe" -ErrorAction SilentlyContinue
}

# ------------------------------------------------------------------ prerequisites

if (-not $SkipDeps) {
    Step 'Checking prerequisites'

    if (-not (Have 'winget')) {
        Warn 'winget is not available, so this script cannot install anything for you.'
        Info 'Install these by hand, then re-run with -SkipDeps:'
        Info '  Go 1.22+   https://go.dev/dl/'
        Info '  MSYS2      https://www.msys2.org/   then: pacman -S mingw-w64-x86_64-gcc'
        Die  'Missing winget.'
    }

    # Everything here comes from the winget community repository. If that source
    # is absent, only msstore is left, which cannot serve these packages and is
    # disabled outright on many managed machines.
    $sources = (winget source list) 2>$null | Out-String
    if ($sources -notmatch '(?m)^\s*winget\s') {
        Warn 'The "winget" package source is not configured on this machine.'
        Info 'Add it with:'
        Info '    winget source add --name winget --arg https://cdn.winget.microsoft.com/cache --type Microsoft.PreIndexed.Package'
        Info 'Or install the prerequisites by hand and re-run with -SkipDeps:'
        Info '  Go 1.22+   https://go.dev/dl/'
        Info '  MSYS2      https://www.msys2.org/   then: pacman -S mingw-w64-x86_64-gcc'
        Die  'No usable winget source.'
    }

    if (-not (Have 'go')) {
        Install-WingetPackage -Id 'GoLang.Go' -Label 'Go' | Out-Null
        Update-PathFromRegistry
    }

}

# --------------------------------------------------------------- toolchain check
#
# This runs even with -SkipDeps. Skipping dependency INSTALLATION should not mean
# skipping dependency DISCOVERY: an existing compiler still has to be located and
# put on PATH, or cgo fails at build time with "gcc: executable file not found".

Step 'Locating the toolchain'

Find-Go | Out-Null
if (-not (Have 'go')) {
    Die 'Go is not on PATH. Install it from https://go.dev/dl/, open a new terminal, and re-run.'
}

# Fyne 2.6 needs Go 1.22 or newer.
$goVer = (go env GOVERSION) -replace '^go', ''
$parts = $goVer.Split('.')
if ([int]$parts[0] -lt 1 -or ([int]$parts[0] -eq 1 -and [int]$parts[1] -lt 22)) {
    Die "Go $goVer is too old; this needs 1.22 or newer. Get it from https://go.dev/dl/"
}
Info "Go ${goVer}: ok  ($((Get-Command go).Source))"

Initialize-Gcc
if (-not (Have 'gcc')) {
    Warn 'No C compiler found. Fyne needs one, because it binds to the native UI through cgo.'
    Info ''
    Info 'Check what you already have:'
    Info '    Get-ChildItem C:\msys64\*\bin\gcc.exe'
    Info ''
    Info 'If that prints a path, pass its folder:'
    Info '    .\build.ps1 -CC "C:\msys64\ucrt64\bin"'
    Info ''
    Info 'Otherwise the simplest option is a standalone toolchain, no MSYS2 needed:'
    Info '    https://winlibs.com/  — download the UCRT zip, extract to C:\mingw64,'
    Info '    then:  .\build.ps1 -CC "C:\mingw64\bin"'
    Die  'No C compiler.'
}
Info "gcc: ok  ($($env:CC))"

# ------------------------------------------------------------------------- build

if (-not $SkipDeps) {
    Step 'Installing the fyne packaging tool'
    # Moved out of the main module at Fyne 2.5; fyne.io/fyne/v2/cmd/fyne is
    # deprecated. Non-fatal, because it only embeds the icon.
    go install fyne.io/tools/cmd/fyne@latest
    if ($LASTEXITCODE -ne 0) {
        Warn 'Could not install the fyne tool. Building without icon embedding.'
    } else {
        Info 'fyne tool: ok'
    }
}
$env:Path = "$(go env GOPATH)\bin;$env:Path"

Step 'Resolving Go modules'
go mod tidy
if ($LASTEXITCODE -ne 0) { Die 'go mod tidy failed.' }

New-Item -ItemType Directory -Force -Path $Dist | Out-Null

Step 'Building Signal Station for Windows'
Info 'The first Fyne build compiles a lot of C and can take several minutes.'

$env:CGO_ENABLED = '1'
Info "CGO_ENABLED=1  CC=$env:CC"
$fyneExe = Join-Path (go env GOPATH) 'bin\fyne.exe'
$packaged = $false

if (Test-Path $fyneExe) {
    & $fyneExe package -os windows -icon Icon.png `
        -name $AppName -appID $AppId -appVersion $AppVersion
    if ($LASTEXITCODE -eq 0 -and (Test-Path "$AppName.exe")) {
        Move-Item -Force "$AppName.exe" (Join-Path $Dist "$AppName.exe")
        $packaged = $true
    } else {
        Warn 'fyne package failed; falling back to a plain build.'
    }
}

if (-not $packaged) {
    # -H=windowsgui stops a console window opening behind the app.
    go build -trimpath -ldflags "-s -w -H=windowsgui" -o (Join-Path $Dist "$BinName.exe") .
    if ($LASTEXITCODE -ne 0) { Die 'Build failed.' }
}

Step 'Done'
Get-ChildItem $Dist | ForEach-Object {
    Write-Host ("    {0,-38} {1,8:N0} KB" -f $_.Name, ($_.Length / 1KB))
}

Write-Host ''
Info 'No macOS build: that requires a Mac. Run .\build.ps1 -Mac for the options.'
