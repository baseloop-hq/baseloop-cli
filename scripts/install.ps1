param(
  [switch]$DryRun,
  [switch]$NoColor,
  [switch]$Help
)

$ErrorActionPreference = 'Stop'

try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
  # Ignore when the runtime manages TLS defaults.
}

if ($env:BASELOOP_DRY_RUN -eq '1') { $DryRun = $true }

$Repo = if ($env:BASELOOP_REPO) { $env:BASELOOP_REPO } else { 'baseloop-hq/baseloop-cli' }
$Version = $env:BASELOOP_VERSION
$SkipSetup = $env:BASELOOP_SKIP_SETUP
$SkipAuth = $env:BASELOOP_SKIP_AUTH
$AutoUpdate = $env:BASELOOP_AUTO_UPDATE
$BinDir = $env:BASELOOP_BIN_DIR
$ApiUrl = $env:BASELOOP_API_URL

# Color is on by default; -NoColor or $env:NO_COLOR turns it off, and
# $env:BASELOOP_FORCE_COLOR=1 forces it back on (for previews/screenshots).
$script:UseColor = $true
if ($NoColor -or $env:NO_COLOR) { $script:UseColor = $false }
if ($env:BASELOOP_FORCE_COLOR -eq '1') { $script:UseColor = $true }
$script:Esc = [char]27
$script:BrandOrange = "$($script:Esc)[38;2;255;79;0m"
$script:Reset = "$($script:Esc)[0m"

if ($script:UseColor) {
  $script:GOk = [char]0x2713; $script:GArr = [char]0x2192
  $script:GWarn = [char]0x26A0; $script:GErr = [char]0x2717; $script:GDot = [char]0x2022
} else {
  $script:GOk = 'OK'; $script:GArr = '->'; $script:GWarn = '!'; $script:GErr = 'ERROR'; $script:GDot = '*'
}

function Write-Color([string]$Text, [string]$Color) {
  if ($script:UseColor) { Write-Host $Text -ForegroundColor $Color } else { Write-Host $Text }
}

function Info([string]$Message) { Write-Color "  $($script:GOk) $Message" Green }
function Warn([string]$Message) { Write-Color "  $($script:GWarn) $Message" Yellow }
function Step([string]$Message) { Write-Color "    $($script:GArr) $Message" DarkGray }
function Detail([string]$Message) { Write-Color "    $Message" DarkGray }
function CommandHint([string]$Prefix, [string]$Command) {
  Write-Host '    ' -NoNewline
  if ($script:UseColor) {
    Write-Host $Prefix -ForegroundColor DarkGray -NoNewline
    Write-Host ' ' -NoNewline
    Write-Host $Command -ForegroundColor Green
  } else {
    Write-Host "$Prefix $Command"
  }
}
function Write-Brand([string]$Text) {
  if ($script:UseColor) {
    Write-Host "  $($script:BrandOrange)$Text$($script:Reset)"
  } else {
    Write-Host "  $Text"
  }
}

function Fail([string]$Message) {
  Write-Color "  $($script:GErr) $Message" Red
  throw $Message
}

function Show-Help {
@"
Baseloop CLI installer

Usage:
  irm https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/install.ps1 | iex
  .\install.ps1 [-DryRun] [-NoColor] [-Help]

  To pass switches through a piped install:
  & ([scriptblock]::Create((irm https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/install.ps1))) -DryRun

Options:
  -DryRun     Preview what the installer would do without changing anything.
              No download, no PATH edits, no files written.
  -NoColor    Disable colored output.
  -Help       Show this help and exit.

Common environment variables:
  BASELOOP_BIN_DIR        Install directory (default: ~\bin or ~\.local\bin)
  BASELOOP_VERSION        Version to install without the v prefix (default: latest)
  BASELOOP_API_URL        API URL used for auth bootstrap
  BASELOOP_SKIP_SETUP     Set to 1 to skip agent (Claude/Codex) setup
  BASELOOP_SKIP_AUTH      Set to 1 to skip the auth bootstrap
  BASELOOP_AUTO_UPDATE    Set to 1 to enable background self-updates
  BASELOOP_FORCE_COLOR    Set to 1 to force colored output (e.g. for previews)

Change your mind later? Run: baseloop uninstall
"@ | Write-Host
}

function Show-Banner {
  $width = 80
  try { $width = [Console]::WindowWidth } catch { $width = 80 }
  if ($width -le 0) { $width = 80 }

  Write-Host ''
  if ($width -ge 70) {
    # BASELOOP wordmark (figlet larry3d). Single-quoted here-string keeps the
    # backticks and quotes literal.
    $word = @'
 ____     ______  ____    ____    __       _____   _____   ____
/\  _`\  /\  _  \/\  _`\ /\  _`\ /\ \     /\  __`\/\  __`\/\  _`\
\ \ \L\ \\ \ \L\ \ \,\L\_\ \ \L\_\ \ \    \ \ \/\ \ \ \/\ \ \ \L\ \
 \ \  _ <'\ \  __ \/_\__ \\ \  _\L\ \ \  __\ \ \ \ \ \ \ \ \ \ ,__/
  \ \ \L\ \\ \ \/\ \/\ \L\ \ \ \L\ \ \ \L\ \\ \ \_\ \ \ \_\ \ \ \/
   \ \____/ \ \_\ \_\ `\____\ \____/\ \____/ \ \_____\ \_____\ \_\
    \/___/   \/_/\/_/\/_____/\/___/  \/___/   \/_____/\/_____/\/_/
'@
    foreach ($line in ($word -split "`n")) {
      Write-Brand $line.TrimEnd("`r")
    }
  } else {
    Write-Brand 'BASELOOP'
  }
  Write-Host ''
  Write-Color '  Bring Baseloop workflows into your AI assistant' DarkGray
  Write-Host ''
}

# A short, friendly "here's exactly what happens" intro. The point is trust:
# tell people up front that this needs no admin rights and is reversible.
function Write-Bullet([string]$Text) {
  Write-Host '    ' -NoNewline
  if ($script:UseColor) { Write-Host $script:GDot -ForegroundColor Cyan -NoNewline } else { Write-Host $script:GDot -NoNewline }
  Write-Host " $Text"
}

function Show-Welcome {
  Write-Host '  ' -NoNewline
  if ($script:UseColor) { Write-Host "Let's get you set up." -ForegroundColor White -NoNewline } else { Write-Host "Let's get you set up." -NoNewline }
  Write-Host ' This usually takes less than a minute.'
  Write-Host ''
  Write-Host '  What this will do:'
  Write-Host ''
  Write-Bullet 'download the official Baseloop CLI and verify it'
  Write-Bullet 'place it in your home folder, no admin password needed'
  Write-Bullet 'add Baseloop shortcuts to your AI assistant'
  Write-Host '    ' -NoNewline
  if ($script:UseColor) { Write-Host $script:GDot -ForegroundColor Cyan -NoNewline } else { Write-Host $script:GDot -NoNewline }
  Write-Host ' keep it reversible: ' -NoNewline
  if ($script:UseColor) { Write-Host 'baseloop uninstall' -ForegroundColor Green -NoNewline } else { Write-Host 'baseloop uninstall' -NoNewline }
  Write-Host ' removes it later'
  Write-Host ''
  if ($DryRun) {
    Write-Color '  -> dry run, this is just a preview, nothing will be installed' DarkGray
  }
}

function Get-PlatformArch {
  $arch = $env:PROCESSOR_ARCHITECTURE
  if ($env:PROCESSOR_ARCHITEW6432) {
    $arch = $env:PROCESSOR_ARCHITEW6432
  }

  switch -Regex ($arch) {
    '^(AMD64|x86_64)$' { return 'amd64' }
    '^ARM64$' { return 'arm64' }
    default { Fail "Unsupported Windows architecture: $arch" }
  }
}

function Get-PlatformLabel([string]$Arch) {
  switch ($Arch) {
    'amd64' { return 'Windows on Intel/AMD' }
    'arm64' { return 'Windows on ARM64' }
    default { return "Windows on $Arch" }
  }
}

function Get-LatestVersion {
  $location = $null
  try {
    $response = Invoke-WebRequest -MaximumRedirection 0 -UseBasicParsing `
      -Headers @{ 'User-Agent' = 'baseloop-cli-installer' } `
      -Uri "https://github.com/$Repo/releases/latest" -ErrorAction Stop
    $location = $response.Headers.Location
  } catch {
    if ($_.Exception.Response) {
      $location = $_.Exception.Response.Headers.Location
      if (-not $location) {
        $location = $_.Exception.Response.Headers['Location']
      }
    }
  }

  if ($location) {
    $tag = ([string]$location).TrimEnd('/').Split('/')[-1]
    $candidate = $tag.TrimStart('v')
    if ($candidate -match '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') {
      return $candidate
    }
  }

  $release = Invoke-RestMethod -ErrorAction Stop `
    -Headers @{ 'User-Agent' = 'baseloop-cli-installer' } `
    -Uri "https://api.github.com/repos/$Repo/releases/latest"
  if (-not $release.tag_name) {
    Fail 'Could not determine latest release version from GitHub.'
  }

  return $release.tag_name.TrimStart('v')
}

function Download-File([string]$Url, [string]$Destination) {
  Invoke-WebRequest -UseBasicParsing -ErrorAction Stop `
    -Headers @{ 'User-Agent' = 'baseloop-cli-installer' } `
    -Uri $Url -OutFile $Destination
}

function Verify-Checksum([string]$ChecksumsPath, [string]$ArchivePath, [string]$ArchiveName) {
  $expected = $null
  foreach ($line in Get-Content $ChecksumsPath) {
    if ($line -match '^(?<hash>[0-9a-fA-F]{64})\s+\*?(?<name>.+)$') {
      if ($Matches.name -eq $ArchiveName) {
        $expected = $Matches.hash.ToLowerInvariant()
        break
      }
    }
  }

  if (-not $expected) {
    Fail "Could not find checksum entry for $ArchiveName"
  }

  $actual = (Get-FileHash -Algorithm SHA256 -Path $ArchivePath).Hash.ToLowerInvariant()
  if ($actual -ne $expected) {
    Fail "Checksum verification failed for $ArchiveName"
  }

}

function Get-PathEntries {
  param([string]$PathValue)

  if (-not $PathValue) {
    return @()
  }

  return $PathValue -split ';' | Where-Object { $_ }
}

function Normalize-PathEntry([string]$PathValue) {
  if (-not $PathValue) {
    return ''
  }

  return $PathValue.Trim().TrimEnd('\')
}

function Get-DefaultBinDir {
  $currentPathEntries = Get-PathEntries $env:Path
  $userPathEntries = Get-PathEntries ([Environment]::GetEnvironmentVariable('Path', 'User'))
  $allEntries = @($currentPathEntries + $userPathEntries) | ForEach-Object { Normalize-PathEntry $_ }

  $homeBin = Normalize-PathEntry (Join-Path $HOME 'bin')
  $homeLocalBin = Normalize-PathEntry (Join-Path $HOME '.local\bin')

  if ($allEntries -contains $homeBin) {
    return $homeBin
  }

  if ($allEntries -contains $homeLocalBin) {
    return $homeLocalBin
  }

  return $homeBin
}

function Ensure-UserPath([string]$Dir) {
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $segments = Get-PathEntries $userPath

  $normalizedSegments = $segments | ForEach-Object { Normalize-PathEntry $_ }
  $normalizedDir = Normalize-PathEntry $Dir
  if ($normalizedSegments -contains $normalizedDir) {
    $currentSegments = Get-PathEntries $env:Path
    $normalizedCurrentSegments = $currentSegments | ForEach-Object { Normalize-PathEntry $_ }
    if ($normalizedCurrentSegments -notcontains $normalizedDir) {
      $env:Path = "$Dir;$env:Path"
    }
    return $false
  }

  $newPath = if ($userPath) { "$Dir;$userPath" } else { $Dir }
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  $env:Path = "$Dir;$env:Path"
  return $true
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

function Format-WorkflowPrompt([string]$Prompt) {
  # Strip control characters before display (defuses ANSI-escape smuggling
  # from a browser-supplied string), mirroring install.sh. Shown literally,
  # with no quote wrapping, so pasting it into the agent keeps the /baseloop
  # slash command intact.
  $Prompt = $Prompt -replace '[\x00-\x1f\x7f]', ''
  return $Prompt
}

function Show-WorkflowPrompt([string]$Prompt) {
  $displayPrompt = Format-WorkflowPrompt -Prompt $Prompt
  if ($script:UseColor) {
    Write-Host "      $displayPrompt" -ForegroundColor Cyan
  } else {
    Write-Host "      $displayPrompt"
  }
}

function Invoke-PendingWorkflow([string]$PromptFile) {
  # Only launch the per-session prompt file this run asked the CLI to write.
  # Falling back to the shared state-dir file could replay a stale prompt
  # parked by an unrelated earlier signup.
  if (-not $PromptFile) {
    return
  }
  $promptFile = $PromptFile
  if (-not (Test-Path -LiteralPath $promptFile)) {
    return
  }

  try {
    $prompt = Get-Content -LiteralPath $promptFile -Raw
    Remove-Item -LiteralPath $promptFile -Force -ErrorAction SilentlyContinue
  } catch {
    Warn "Could not read the pending workflow prompt."
    return
  }

  if ($null -eq $prompt) {
    return
  }
  $prompt = $prompt.Trim()
  if (-not $prompt) {
    return
  }
  if ($prompt.StartsWith('-')) {
    # A flag-shaped prompt would be parsed by the agent CLI as an option,
    # not a prompt. Never launch one.
    return
  }

  $agent = Get-Command claude -ErrorAction SilentlyContinue
  if (-not $agent) {
    $agent = Get-Command codex -ErrorAction SilentlyContinue
  }

  if (-not $agent) {
    Info 'Claude Code is not installed, so the workflow was not started.'
    Detail 'Install Claude Code (or Codex), then paste this workflow prompt:'
    Show-WorkflowPrompt -Prompt $prompt
    return
  }

  $agentName = $agent.Name
  $agentPath = $agent.Source
  if (-not $agentPath) {
    $agentPath = $agent.Path
  }

  Write-Host ''
  Write-Color '  Workflow received' Green
  Show-WorkflowPrompt -Prompt $prompt
  Write-Host ''
  $answer = Read-Host "  Press Enter to run it with $agentName now [Enter/n]"
  if ($answer -match '^(?i:n|no)$') {
    Detail "Skipped. Start $agentName, then paste this workflow prompt:"
    Show-WorkflowPrompt -Prompt $prompt
    return
  }

  try {
    & $agentPath $prompt
    if ($LASTEXITCODE -ne 0) {
      Warn "$agentName exited before the workflow completed."
      Detail "Start $agentName, then paste this workflow prompt:"
      Show-WorkflowPrompt -Prompt $prompt
    }
  } catch {
    Warn "Could not start $agentName."
    Detail "Start $agentName, then paste this workflow prompt:"
    Show-WorkflowPrompt -Prompt $prompt
  }
}

function Save-UserPathInstallState([string]$Dir) {
  if (-not $Dir) { return }

  $stateDir = Get-StateDir
  $manifestPath = Join-Path $stateDir 'manifest.json'
  New-Item -ItemType Directory -Force -Path $stateDir | Out-Null

  $manifest = [ordered]@{ schema = 1 }
  if (Test-Path $manifestPath) {
    try {
      $existing = Get-Content -Raw -Path $manifestPath | ConvertFrom-Json
      $existing.PSObject.Properties | ForEach-Object { $manifest[$_.Name] = $_.Value }
    } catch {
      Warn "Could not read existing install manifest; rewriting PATH state only."
    }
  }

  $entries = @()
  if ($manifest.Keys -contains 'windows_user_path_entries') {
    $entries = @($manifest['windows_user_path_entries'])
  }
  $normalizedDir = Normalize-PathEntry $Dir
  if (($entries | ForEach-Object { Normalize-PathEntry $_ }) -notcontains $normalizedDir) {
    $entries += $normalizedDir
  }
  $manifest['windows_user_path_entries'] = $entries
  $manifest | ConvertTo-Json -Depth 5 | Set-Content -Path $manifestPath -Encoding UTF8
}

function Install-AgentSkills([string]$InstalledBinary) {
  if ($SkipSetup -eq '1') {
    Step 'skipping agent setup (BASELOOP_SKIP_SETUP=1)'
    return
  }

  if ($DryRun) {
    Step 'would install the Baseloop entry skills and plugins for Claude and Codex'
    Info 'would add Baseloop agent setup'
    return
  }

  # Capture instead of stream: on success the binary's summary line duplicates
  # the Info below, but on failure its error and hint must reach the user.
  # The CLI's own error text names the failing agent, so this stays neutral.
  $setupOutput = & $InstalledBinary setup skills 2>&1
  if ($LASTEXITCODE -eq 0) {
    Info 'Baseloop agent setup added'
    return
  }

  Warn 'Baseloop agent setup was not added'
  CommandHint 'Retry after fixing the agent named below with:' 'baseloop setup skills'
  $setupOutput | ForEach-Object { Write-Host "      $_" }
  Fail 'Baseloop agent setup failed.'
}

# Opt-in fleet hook: BASELOOP_AUTO_UPDATE=1 at install time turns on
# background self-updates for this machine. Best-effort: a failed enable must
# not fail the install.
function Enable-AutoUpdate([string]$InstalledBinary) {
  if ($AutoUpdate -ne '1') {
    return
  }

  if ($DryRun) {
    Step 'would enable background auto-update'
    return
  }

  if ($env:BASELOOP_REPO -and $env:BASELOOP_REPO -ne 'baseloop-hq/baseloop-cli') {
    Warn 'BASELOOP_REPO is set: automatic updates only trust the official repo, so this install will show update notices instead of self-updating'
  }

  & $InstalledBinary setup auto-update on *> $null
  if ($LASTEXITCODE -eq 0) {
    Info 'background auto-update enabled'
  } else {
    Warn 'could not enable auto-update; run: baseloop setup auto-update on'
  }
}

# Returns $true when sign-in completed, $false otherwise. All user-facing
# output here goes through Write-Host (Info/Warn/Step/Detail), so the bool is
# the only value emitted to the pipeline.
function Bootstrap-Auth([string]$InstalledBinary) {
  $authArgs = @('auth', 'login')
  $workflowPromptFile = ''

  if ($SkipAuth -eq '1') {
    Info 'Sign-in skipped for this install'
    return $false
  }

  if ($DryRun) {
    Step 'would open a browser to connect your Baseloop account'
    return $false
  }

  try {
    if ([Console]::IsOutputRedirected) {
      Info 'Sign-in skipped for now'
      return $false
    }
  } catch {
    Info 'Sign-in skipped for now'
    return $false
  }

  Detail "Answer no if you're new to Baseloop; we'll open signup and connect this CLI."
  while ($true) {
    $answer = Read-Host '  Do you already have a Baseloop account? [y/N]'
    switch -Regex ($answer) {
      '^(?i:y|yes)$' {
        break
      }
      '^(?i:|n|no)$' {
        $authArgs = @('auth', 'login', '--signup')
        Detail "No problem, we'll open Baseloop so you can create one and connect this CLI."
        break
      }
      default {
        Warn 'Please answer y or n'
      }
    }

    if ($answer -match '^(?i:y|yes|n|no)?$') {
      break
    }
  }

  if ($ApiUrl) {
    $authArgs += @('--api-url', $ApiUrl)
  }

  if ($authArgs -contains '--signup') {
    $stateDir = Get-StateDir
    New-Item -ItemType Directory -Force -Path $stateDir | Out-Null
    Remove-Item -LiteralPath (Join-Path $stateDir 'workflow-prompt') -Force -ErrorAction SilentlyContinue
    $workflowPromptFile = Join-Path $stateDir ('workflow-prompt-' + [guid]::NewGuid().ToString('N'))
    Remove-Item -LiteralPath $workflowPromptFile -Force -ErrorAction SilentlyContinue
  }

  # Indent the binary's output so it sits inside the installer's layout, and
  # drop its final "Authenticated..." line; the Info below replaces it. The
  # browser-fallback URL still comes through.
  $oldWorkflowPromptFile = $env:BASELOOP_WORKFLOW_PROMPT_FILE
  if ($workflowPromptFile) {
    $env:BASELOOP_WORKFLOW_PROMPT_FILE = $workflowPromptFile
  }
  try {
    & $InstalledBinary @authArgs 2>&1 | ForEach-Object {
      $line = "$_"
      if ($line -match '^Authenticated\.') {
        return
      } elseif ($line -eq 'Opening Baseloop login in your browser...') {
        Detail 'A browser window should open.'
      } elseif ($line -eq 'Closed the window by accident? Use this link:') {
        Detail 'If the browser did not open, copy this link:'
      } elseif ($line -match '^https?://') {
        Write-Host '      ' -NoNewline
        if ($script:UseColor) { Write-Host $line -ForegroundColor Cyan } else { Write-Host $line }
      } elseif ($line) {
        Detail $line
      }
    }
  } finally {
    if ($null -eq $oldWorkflowPromptFile) {
      Remove-Item Env:BASELOOP_WORKFLOW_PROMPT_FILE -ErrorAction SilentlyContinue
    } else {
      $env:BASELOOP_WORKFLOW_PROMPT_FILE = $oldWorkflowPromptFile
    }
  }
  if ($LASTEXITCODE -ne 0) {
    Warn "Sign-in didn't complete"
    CommandHint 'You can run it anytime:' 'baseloop auth login'
    return $false
  }
  Info 'Connected your Baseloop account'
  Invoke-PendingWorkflow -PromptFile $workflowPromptFile
  return $true
}

function Print-Success([string]$InstalledBinary, [bool]$PathAdded, [bool]$Authenticated) {
  if ($DryRun) {
    Write-Host ''
    Info 'Dry run complete, nothing was changed on your machine.'
    Write-Host "     Run the same command without -DryRun when you're ready."
    Write-Host ''
    return
  }

  Write-Host ''
  Write-Color "  You're all set! Baseloop is ready to go." Green
  Write-Host ''
  Write-Host '  Next step'
  if (-not $Authenticated) {
    Write-Host '    Sign in to your Baseloop account first:'
    Write-Host '    ' -NoNewline
    if ($script:UseColor) { Write-Host 'baseloop auth login' -ForegroundColor Cyan } else { Write-Host 'baseloop auth login' }
    Write-Host ''
    Write-Host '    Then open your AI assistant and type:'
  } else {
    Write-Host '    Open your AI assistant and type:'
  }
  Write-Host '    ' -NoNewline
  if ($script:UseColor) { Write-Host '/baseloop list my Baseloop workspaces' -ForegroundColor Cyan } else { Write-Host '/baseloop list my Baseloop workspaces' }
  Write-Host ''
  Write-Host '  Using Claude Cowork (desktop app)? Skills work via a plugin there, setup takes a minute:'
  Write-Host '    https://github.com/baseloop-hq/baseloop-gtm-plugin'
  Write-Host ''
  Write-Color '  Changed your mind? Baseloop can be removed later with the uninstaller.' DarkGray
  Write-Host ''
  if ($PathAdded) {
    Write-Color '  Open a new terminal before running baseloop directly.' Yellow
  } else {
    Write-Host '  Enjoy!'
  }
  Write-Host ''
}

function Main {
  if ($Help) { Show-Help; return }

  Show-Banner
  Show-Welcome

  $arch = Get-PlatformArch
  if (-not $BinDir) {
    $script:BinDir = Get-DefaultBinDir
  }
  $installedBinary = Join-Path $BinDir 'baseloop.exe'
  Detail "detected $(Get-PlatformLabel $arch)"

  if ($DryRun) {
    $resolvedVersion = if ($Version) { $Version } else { '<latest>' }
  } else {
    $resolvedVersion = if ($Version) { $Version } else { Get-LatestVersion }
  }
  if ($resolvedVersion -ne '<latest>' -and $resolvedVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') {
    Fail "Invalid version '$resolvedVersion'. Expected semver format like 1.2.3 or 1.2.3-rc.1."
  }

  $archiveName = "baseloop_${resolvedVersion}_windows_${arch}.zip"
  $baseUrl = "https://github.com/$Repo/releases/download/v$resolvedVersion"

  if ($DryRun) {
    Step "would download the Baseloop build for $(Get-PlatformLabel $arch)"
    Step 'would verify it before installing'
    Info "would install Baseloop on this computer"
  } else {
    $tmpDir = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $tmpDir | Out-Null
    try {
      $archivePath = Join-Path $tmpDir $archiveName
      $checksumsPath = Join-Path $tmpDir 'checksums.txt'
      $extractDir = Join-Path $tmpDir 'extract'

      Download-File -Url "$baseUrl/$archiveName" -Destination $archivePath

      Download-File -Url "$baseUrl/checksums.txt" -Destination $checksumsPath
      Verify-Checksum -ChecksumsPath $checksumsPath -ArchivePath $archivePath -ArchiveName $archiveName

      Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force

      $binaryPath = Join-Path $extractDir 'baseloop.exe'
      if (-not (Test-Path $binaryPath)) {
        Fail 'baseloop.exe not found in archive'
      }

      New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
      try {
        Copy-Item -Force $binaryPath $installedBinary -ErrorAction Stop
      } catch {
        Fail "Failed to install baseloop.exe. If it is in use, close any running baseloop processes and re-run the installer. Original error: $($_.Exception.Message)"
      }
      Info "Baseloop downloaded and installed"
    }
    finally {
      if (Test-Path $tmpDir) {
        Remove-Item -Recurse -Force $tmpDir
      }
    }
  }

  $pathAdded = $false
  if ($DryRun) {
    Info "would add the baseloop command"
  } else {
    $pathAdded = Ensure-UserPath -Dir $BinDir
    if ($pathAdded) {
      Save-UserPathInstallState -Dir $BinDir
      Info "Added the baseloop command"
    } else {
      Info "baseloop command already available"
    }
  }

  if ($DryRun) {
    Info 'would check that Baseloop opens'
  } else {
    & $installedBinary --version | Out-Null
    if ($LASTEXITCODE -ne 0) {
      Fail 'Installation failed; baseloop is not working'
    }
    Info "Baseloop opens correctly"
  }

  Install-AgentSkills -InstalledBinary $installedBinary

  Enable-AutoUpdate -InstalledBinary $installedBinary

  $authenticated = Bootstrap-Auth -InstalledBinary $installedBinary

  Print-Success -InstalledBinary $installedBinary -PathAdded $pathAdded -Authenticated $authenticated
}

Main
