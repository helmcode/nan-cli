#Requires -Version 5.1
<#
.SYNOPSIS
  Installs the nan.builders CLI on Windows.

.DESCRIPTION
  The Windows half of scripts/install.sh. It resolves the latest release,
  downloads the .zip for this architecture, verifies its checksum against
  checksums.txt and puts nan.exe somewhere on the PATH.

  Two things differ from the bash one, both because Windows differs:

  - The artifact is a .zip, not a .tar.gz. Nothing on a stock Windows unpacks
    a tarball by double-clicking, and Expand-Archive is built in.
  - The default install directory is per-user (LOCALAPPDATA\Programs\nan) and
    not a machine-wide one. /usr/local/bin has a sudo prompt that a person
    expects; the Windows equivalent is an elevation dialog out of a piped
    script, which is worse than installing for one user. Set -InstallDir to
    override.

.PARAMETER Version
  A tag such as v0.1.3. Defaults to the latest release.

.PARAMETER InstallDir
  Where to put nan.exe. Defaults to $env:LOCALAPPDATA\Programs\nan.

.EXAMPLE
  irm https://nan.builders/install.ps1 | iex

.EXAMPLE
  & ([scriptblock]::Create((irm https://nan.builders/install.ps1))) -Version v0.1.4
#>
[CmdletBinding()]
param(
  [string]$Version = $env:NAN_VERSION,
  [string]$InstallDir = $env:NAN_INSTALL_DIR
)

# The preferences below are set INSIDE the function, not here.
#
# `iex` runs this in the caller's session, so an assignment at this level is an
# assignment to their shell, for the rest of its life. $ErrorActionPreference =
# 'Stop' left behind that way turns every later non-terminating error in that
# session into a terminating one - including inside the `prompt` function a
# terminal like Warp installs to know where a command begins and ends. When
# that throws, PowerShell falls back to its built-in `PS>` and the terminal
# loses track of the session: the prompt is there and nothing typed at it does
# anything. Which is exactly what was reported, twice.
#
# Inside a function the same assignment is local and goes away with the call.

$Repo = 'helmcode/nan-cli'

function Write-Step($message) { Write-Host "> $message" -ForegroundColor Cyan }
function Write-Done($message) { Write-Host "OK $message" -ForegroundColor Green }
function Write-Warn($message) { Write-Host "!  $message" -ForegroundColor Yellow }
function Write-Fail($message) { Write-Host "x  $message" -ForegroundColor Red }

function Get-Arch {
  # Two sources, because the better one is not always reachable.
  #
  # RuntimeInformation.OSArchitecture is the accurate answer: it reports the
  # OS, and the OS is what decides which binary runs. But reaching for an
  # arbitrary .NET type is exactly what ConstrainedLanguage mode blocks, which
  # is the normal state of a machine under an AppLocker or WDAC policy, and the
  # type does not exist at all before .NET Framework 4.7.1. In either case this
  # used to leave $arch empty and report the machine as unsupported - a
  # dead end over something that was never about the architecture.
  $osArch = $null
  try {
    $osArch = "$([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)"
  } catch {
    $osArch = $null
  }

  # The environment answers the same question without touching .NET.
  # PROCESSOR_ARCHITEW6432 is set only inside a 32-bit process on a 64-bit OS
  # and carries the real OS architecture; PROCESSOR_ARCHITECTURE carries the
  # process one. Read in that order they give the OS answer either way, which
  # is why a 32-bit PowerShell on an arm64 machine does not end up asking for
  # an x86 build that does not exist.
  if (-not $osArch) {
    $osArch = $env:PROCESSOR_ARCHITEW6432
    if (-not $osArch) { $osArch = $env:PROCESSOR_ARCHITECTURE }
  }

  # -Regex over an explicitly stringified value, rather than a switch on the
  # enum: it takes both spellings of each architecture and does not depend on
  # how a given PowerShell renders an enum it may not have been able to load.
  switch -Regex ("$osArch".Trim().ToUpperInvariant()) {
    '^(X64|AMD64)$'    { return 'amd64' }
    '^(ARM64|AARCH64)$' { return 'arm64' }
  }

  throw @"
unsupported architecture: '$osArch'
the releases carry amd64 and arm64: https://github.com/$Repo/releases
if that value looks wrong for your machine, it is a bug in this installer:
https://github.com/$Repo/issues
"@
}

function Get-LatestVersion {
  try {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{
      'User-Agent' = 'nan-cli-installer'
    }
  } catch {
    $release = $null
  }
  if ($release -and $release.tag_name) {
    return $release.tag_name
  }
  # The GitHub API rate limits unauthenticated requests per IP, so this fails
  # for reasons that have nothing to do with this repo. Saying so beats letting
  # an empty version go into a URL and reporting a 404 from it.
  throw @"
could not work out the latest version from the GitHub API
it rate limits unauthenticated requests, so this is usually temporary
wait a few minutes, or pick a version yourself:
    & ([scriptblock]::Create((irm https://nan.builders/install.ps1))) -Version v0.1.12
the releases are at https://github.com/$Repo/releases
"@
}

function Assert-Checksum($file, $expected) {
  if (-not $expected) {
    throw 'checksums.txt carries no entry for this archive'
  }
  $actual = (Get-FileHash -Path $file -Algorithm SHA256).Hash.ToLower()
  if ($actual -ne $expected.ToLower()) {
    throw "checksum mismatch`n  expected: $expected`n  got:      $actual"
  }
}

function Add-ToUserPath($dir) {
  # The user PATH, not the machine one: no elevation, and it survives reboots,
  # which setting it only in this process would not.
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $entries = @()
  if ($userPath) { $entries = $userPath -split ';' | Where-Object { $_ } }
  if ($entries -contains $dir) { return $false }

  [Environment]::SetEnvironmentVariable('Path', (($entries + $dir) -join ';'), 'User')
  # And in this session too, so `nan` works without opening a new terminal.
  $env:Path = "$env:Path;$dir"
  return $true
}

function Install-NanCli {
  param([string]$Version, [string]$InstallDir)

  # Local to this call. See the note where these used to live.
  $ErrorActionPreference = 'Stop'
  $ProgressPreference = 'SilentlyContinue'  # Write-Progress is slow over a pipe

  $arch = Get-Arch
  if (-not $Version) {
    Write-Step 'fetching latest release...'
    $Version = Get-LatestVersion
  }
  if (-not $InstallDir) {
    $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\nan'
  }

  Write-Done "nan-cli $Version (windows/$arch)"

  $archive = "nan-cli_${Version}_windows_${arch}.zip"
  $base = "https://github.com/$Repo/releases/download/$Version"
  $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("nan-install-" + [guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $tmp -Force | Out-Null

  try {
    Write-Step "downloading $archive..."
    try {
      # -UseBasicParsing on every request here. Without it, PowerShell 5.1 hands
      # the body to the Internet Explorer engine to build a DOM, and where that
      # engine is absent or has never been through its first-run setup the call
      # throws a NullReferenceException - which is what "Object reference not set
      # to an instance of an object" means coming out of Invoke-WebRequest.
      Invoke-WebRequest -Uri "$base/$archive" -OutFile (Join-Path $tmp $archive) -UseBasicParsing
    } catch {
      throw "could not download $archive`ncheck that $Version is a published release: https://github.com/$Repo/releases"
    }

    Write-Step 'verifying checksum...'
    # Downloaded to a file rather than read off the response. GitHub serves
    # release assets as application/octet-stream, and for a non-text content type
    # PowerShell hands back .Content as a Byte[], not a string: splitting that on
    # a newline matches nothing and every archive reads as having no checksum.
    # -OutFile takes the bytes as they come and Get-Content decodes them.
    $checksumFile = Join-Path $tmp 'checksums.txt'
    Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $checksumFile -UseBasicParsing
    $expected = $null
    foreach ($line in (Get-Content -Path $checksumFile)) {
      if ($line -match "^([0-9a-fA-F]{64})\s+\*?$([regex]::Escape($archive))\s*$") {
        $expected = $Matches[1]
      }
    }
    Assert-Checksum (Join-Path $tmp $archive) $expected

    Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $tmp -Force
    $binary = Join-Path $tmp 'nan.exe'
    if (-not (Test-Path $binary)) {
      throw 'the archive does not contain nan.exe'
    }

    Write-Step "installing to $(Join-Path $InstallDir 'nan.exe')..."
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    # A running nan.exe holds a lock on its own file, so replacing it while the
    # TUI is open fails with a message about the file being in use. Saying which
    # file and why beats the raw exception.
    try {
      Copy-Item -Path $binary -Destination (Join-Path $InstallDir 'nan.exe') -Force
    } catch {
      throw "could not write $(Join-Path $InstallDir 'nan.exe')`nif nan is running, close it and try again"
    }

    Write-Done "installed $Version to $(Join-Path $InstallDir 'nan.exe')"

    $pathAdded = Add-ToUserPath $InstallDir

    # Numbered, because "Run nan to get started" was true and not enough: it
    # was printed under a warning about restarting, from a shell that could not
    # find `nan` yet, to someone who then had to work out that signing in is a
    # subcommand nothing had mentioned. Three things have to happen in order,
    # so they are listed in order.
    Write-Host ''
    Write-Host 'Next:' -ForegroundColor White
    Write-Host ''
    $step = 0
    $next = {
      param($what, $why)
      $script:step++
      Write-Host ("  {0}. " -f $script:step) -NoNewline
      Write-Host $what.PadRight(24) -ForegroundColor Cyan -NoNewline
      Write-Host $why -ForegroundColor DarkGray
    }
    if ($pathAdded) {
      # Not "open a new tab": a terminal that keeps one process alive for all
      # of its tabs - Warp, Windows Terminal with a running profile - hands
      # each new tab the environment it started with, PATH included.
      & $next 'Restart your terminal' 'close it completely and open it again'
    }
    & $next 'nan' 'open the panel - it signs you in from there'
    Write-Host ''
    if ($pathAdded) {
      Write-Warn "$InstallDir was added to your PATH, which is why the restart matters"
    }
  } finally {
    Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
  }
}

# The entry point, and the reason it is shaped like this.
#
# This script is meant to be run as `irm https://nan.builders/install.ps1 | iex`,
# and `iex` runs what it is given IN THE CURRENT SESSION. An `exit` there does
# not end a script, it ends the session: in a terminal that means the tab
# closes, instantly, taking the error message with it. Every failure path here
# used to do exactly that, so the careful messages about rate limits and
# checksums were written straight into a window that was already gone.
#
# So nothing below the function throws the session away. Failures come back as
# exceptions, get printed, and the prompt is still there afterwards.
try {
  Install-NanCli -Version $Version -InstallDir $InstallDir
} catch {
  foreach ($line in ($_.Exception.Message -split "`n")) {
    Write-Fail $line.TrimEnd()
  }
  # Run as a file (./install.ps1, or from CI) there is no session to protect and
  # a non-zero status is what a caller checks. Piped into iex, MyCommand.Path is
  # empty and exiting would be the very bug this is here to avoid.
  if ($MyInvocation.MyCommand.Path) {
    exit 1
  }
}
