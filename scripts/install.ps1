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

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'  # Write-Progress is slow over a pipe

$Repo = 'helmcode/nan-cli'

function Write-Step($message) { Write-Host "> $message" -ForegroundColor Cyan }
function Write-Done($message) { Write-Host "OK $message" -ForegroundColor Green }
function Write-Warn($message) { Write-Host "!  $message" -ForegroundColor Yellow }
function Write-Fail($message) { Write-Host "x  $message" -ForegroundColor Red }

function Get-Arch {
  # PROCESSOR_ARCHITECTURE reports the architecture of the *process* under
  # WOW64, so a 32-bit PowerShell on an arm64 machine would claim x86. The OS
  # architecture is the one that decides which binary runs.
  $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
  switch ($arch) {
    'X64'   { return 'amd64' }
    'Arm64' { return 'arm64' }
    default {
      Write-Fail "unsupported architecture: $arch"
      Write-Fail "the releases carry amd64 and arm64: https://github.com/$Repo/releases"
      exit 1
    }
  }
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
  Write-Fail 'could not work out the latest version from the GitHub API'
  Write-Fail 'it rate limits unauthenticated requests, so this is usually temporary'
  Write-Fail 'wait a few minutes, or pick a version yourself:'
  Write-Host  '    & ([scriptblock]::Create((irm https://nan.builders/install.ps1))) -Version v0.1.4'
  Write-Fail "the releases are at https://github.com/$Repo/releases"
  exit 1
}

function Assert-Checksum($file, $expected) {
  if (-not $expected) {
    Write-Fail 'checksums.txt carries no entry for this archive'
    exit 1
  }
  $actual = (Get-FileHash -Path $file -Algorithm SHA256).Hash.ToLower()
  if ($actual -ne $expected.ToLower()) {
    Write-Fail 'checksum mismatch'
    Write-Fail "  expected: $expected"
    Write-Fail "  got:      $actual"
    exit 1
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
    Write-Fail "could not download $archive"
    Write-Fail "check that $Version is a published release: https://github.com/$Repo/releases"
    exit 1
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
    Write-Fail 'the archive does not contain nan.exe'
    exit 1
  }

  Write-Step "installing to $InstallDir\nan.exe..."
  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  # A running nan.exe holds a lock on its own file, so replacing it while the
  # TUI is open fails with a message about the file being in use. Saying which
  # file and why beats the raw exception.
  try {
    Copy-Item -Path $binary -Destination (Join-Path $InstallDir 'nan.exe') -Force
  } catch {
    Write-Fail "could not write $InstallDir\nan.exe"
    Write-Fail 'if nan is running, close it and try again'
    exit 1
  }

  Write-Done "installed $Version to $InstallDir\nan.exe"

  if (Add-ToUserPath $InstallDir) {
    Write-Warn "$InstallDir was added to your PATH"
    Write-Warn 'already-open terminals will not see it until they are restarted'
  }
  Write-Host ''
  Write-Host 'Run ' -NoNewline; Write-Host 'nan' -ForegroundColor Cyan -NoNewline; Write-Host ' to get started.'
} finally {
  Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
