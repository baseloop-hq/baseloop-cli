# uninstall.ps1 - Remove the Baseloop CLI and everything its installer added.
#
# Usage:
#   irm https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/uninstall.ps1 | iex
#
# Flags (passed through to `baseloop uninstall`): --dry-run, --purge

$ErrorActionPreference = 'Stop'

$BinDir = $env:BASELOOP_BIN_DIR
$RemovalFailed = $false

function Step([string]$Message) { Write-Host "  -> $Message" }
function Info([string]$Message) { Write-Host "  OK $Message" -ForegroundColor Green }
function Warn([string]$Message) { Write-Host "  WARN $Message" -ForegroundColor Yellow }
function Fail([string]$Message) { throw $Message }

# Capture script-level args; $args is function-scoped, so Main cannot see them.
$ScriptArgs = $args
$DryRun = $ScriptArgs -contains '--dry-run'
$Purge = $ScriptArgs -contains '--purge'

function Get-PathEntries([string]$PathValue) {
  if (-not $PathValue) { return @() }
  return $PathValue -split ';' | Where-Object { $_ }
}

function Normalize-PathEntry([string]$PathValue) {
  if (-not $PathValue) { return '' }
  return $PathValue.Trim().TrimEnd('\')
}

function Find-Binary {
  if ($BinDir -and (Test-Path -LiteralPath (Join-Path $BinDir 'baseloop.exe'))) {
    return (Join-Path $BinDir 'baseloop.exe')
  }
  $command = Get-Command baseloop -ErrorAction SilentlyContinue
  if ($command -and $command.Source) {
    return $command.Source
  }
  foreach ($candidate in @((Join-Path $HOME 'bin\baseloop.exe'), (Join-Path $HOME '.local\bin\baseloop.exe'))) {
    if (Test-Path -LiteralPath $candidate) { return $candidate }
  }
  return $null
}

function Get-StateDir {
  if ($env:BASELOOP_STATE) {
    return $env:BASELOOP_STATE
  }
  if ($env:XDG_STATE_HOME) {
    return (Join-Path $env:XDG_STATE_HOME 'baseloop')
  }
  return (Join-Path $HOME '.local\state\baseloop')
}

function Get-ConfigPath {
  if ($env:BASELOOP_CONFIG) {
    return $env:BASELOOP_CONFIG
  }
  if ($env:XDG_CONFIG_HOME) {
    return (Join-Path $env:XDG_CONFIG_HOME 'baseloop\config.json')
  }
  return (Join-Path $HOME '.config\baseloop\config.json')
}

# One dir per supported agent. The Codex dir honors CODEX_HOME the same way
# the CLI binary does; removal is ownership-gated, so listing a dir that does
# not exist (or is not ours) is harmless.
function Get-BaseloopEntrySkillDirs {
  $codexHome = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $HOME '.codex' }
  return @(
    (Join-Path $HOME '.claude\skills\baseloop'),
    (Join-Path $codexHome 'skills\baseloop')
  )
}

function Test-BaseloopEntrySkillDir([string]$Path) {
  $skillFile = Join-Path $Path 'SKILL.md'
  $markerFile = Join-Path $Path '.baseloop.sha256'
  if (-not (Test-Path -LiteralPath $skillFile)) { return $false }
  if (-not (Test-Path -LiteralPath $markerFile)) { return $false }
  try {
    $expected = (Get-Content -Raw -LiteralPath $markerFile).Trim()
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $skillFile).Hash.ToLowerInvariant()
    return $expected -and ($expected.ToLowerInvariant() -eq $actual)
  } catch {
    return $false
  }
}

function Get-InstallManifest {
  $manifestPath = Join-Path (Get-StateDir) 'manifest.json'
  if (-not (Test-Path -LiteralPath $manifestPath)) {
    return $null
  }
  try {
    return Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
  } catch {
    Warn "Could not read install manifest at $manifestPath"
    return $null
  }
}

function Get-RecordedUserPathEntries {
  $manifest = Get-InstallManifest
  if ($manifest -and ($manifest.PSObject.Properties.Name -contains 'windows_user_path_entries')) {
    return @($manifest.windows_user_path_entries)
  }
  return @()
}

function Remove-PathIfExists([string]$Path) {
  if (-not $Path) { return }
  $item = Get-Item -Force -LiteralPath $Path -ErrorAction SilentlyContinue
  if (-not $item) { return }
  if ($DryRun) {
    Info "Would remove $Path"
    return
  }
  try {
    Remove-Item -Recurse -Force -LiteralPath $Path -ErrorAction Stop
    Info "Removed $Path"
  } catch {
    $script:RemovalFailed = $true
    Warn "Could not remove $Path; delete it manually."
  }
}

function Remove-KnownFilesDirectly {
  Step 'Removing known Baseloop files directly'

  $paths = @()
  foreach ($entrySkillDir in Get-BaseloopEntrySkillDirs) {
    if (Test-BaseloopEntrySkillDir -Path $entrySkillDir) {
      $paths += $entrySkillDir
    }
  }
  if ($env:BASELOOP_STATE) {
    $paths += Join-Path (Get-StateDir) 'manifest.json'
  } else {
    $paths += Get-StateDir
  }
  if ($BinDir) {
    $paths += Join-Path $BinDir 'baseloop.exe'
  }
  if ($Purge) {
    $config = Get-ConfigPath
    if ((Split-Path -Leaf $config) -eq 'config.json') {
      $paths += $config
    } else {
      $script:RemovalFailed = $true
      Warn "Refusing to remove unsafe config path $config; expected a config.json file."
    }
  }

  foreach ($path in $paths) {
    Remove-PathIfExists -Path $path
  }

  if ($Purge -and -not $DryRun) {
    $configDir = Split-Path -Parent (Get-ConfigPath)
    if ($configDir -and (Test-Path -LiteralPath $configDir) -and ((Split-Path -Leaf (Get-ConfigPath)) -eq 'config.json')) {
      try {
        Remove-Item -Force -LiteralPath $configDir -ErrorAction SilentlyContinue
      } catch {
        # Intentionally empty: failure is expected when the directory is not empty.
      }
    }
  }
  if ($env:BASELOOP_STATE -and -not $DryRun) {
    try {
      Remove-Item -Force -LiteralPath (Get-StateDir) -ErrorAction SilentlyContinue
    } catch {
      # Intentionally empty: failure is expected when the directory is not empty.
    }
  }
}

function Remove-FromUserPath([string]$Dir) {
  if (-not $Dir) { return }
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $normalizedDir = Normalize-PathEntry $Dir
  $segments = Get-PathEntries $userPath
  $kept = $segments | Where-Object { (Normalize-PathEntry $_) -ne $normalizedDir }
  if (($segments.Count) -ne ($kept.Count)) {
    [Environment]::SetEnvironmentVariable('Path', ($kept -join ';'), 'User')
    Info "Removed $Dir from your user PATH"
  }
}

function Remove-RecordedUserPathEntries([object[]]$Entries) {
  $entries = @($Entries)
  if (-not $entries -or $entries.Count -eq 0) {
    Warn 'No installer-owned Windows PATH entry was recorded; leaving user PATH unchanged.'
    return
  }
  foreach ($entry in $entries) {
    if ($DryRun) {
      Info "Would remove $entry from your user PATH"
    } else {
      Remove-FromUserPath -Dir $entry
    }
  }
}

function Main {
  Write-Host ''
  Write-Host 'Baseloop CLI uninstaller'
  Write-Host ''

  $recordedPathEntries = Get-RecordedUserPathEntries
  $binary = Find-Binary
  if ($binary) {
    Step "Using $binary"
    # --keep-binary: this script removes the binary itself (a running .exe is
    # locked, so the CLI cannot delete itself on Windows).
    & $binary uninstall --keep-binary @ScriptArgs
    if ($LASTEXITCODE -ne 0) { Fail 'baseloop uninstall reported issues; fix them and rerun uninstall.' }
  } else {
    Warn 'baseloop binary not found; removing known files directly.'
    Remove-KnownFilesDirectly
  }

  if ($DryRun) {
    Write-Host ''
    Info 'Dry run complete. Re-run without --dry-run to remove.'
    return
  }

  if ($binary -and (Test-Path -LiteralPath $binary)) {
    try {
      Remove-Item -Force -LiteralPath $binary -ErrorAction Stop
      Info "Removed $binary"
    } catch {
      $script:RemovalFailed = $true
      Warn "Could not remove $binary (it may be in use). Close baseloop processes and delete it manually."
    }
  }

  Remove-RecordedUserPathEntries -Entries $recordedPathEntries

  if ($RemovalFailed) {
    Fail 'Uninstall finished with removal errors; delete the warned paths manually and rerun uninstall.'
  }

  Write-Host ''
  Write-Host 'Baseloop CLI uninstalled.' -ForegroundColor Green
  Write-Host '  Open a new terminal to refresh your PATH.'
  Write-Host ''
}

Main
