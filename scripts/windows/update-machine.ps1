<#
.SYNOPSIS
  Update a Windows machine's tmx-go (daemon + desktop window) to an exact commit: build, test, swap, verify, roll back.
.DESCRIPTION
  Until fleet-v* releases exist this is a machine's update path. Steps:
    1. fetch the commit in a build checkout, build the UI, the daemon and the window (NEVER -tags tmx_ci: that tag
       compiles the guard tests with destructive command strings and Windows Defender kills the process tree);
    2. run the targeted tests (go test -p 2 on the packages in -TestPackages);
    3. (unless -NoSwap) when the owner is active at the machine, warn ON SCREEN -WarnSeconds before the restart;
    4. keep the current exes as <name>.prev-<oldcommit>.exe, swap in the new ones, `tmx-go down`; the machine's
       watchdog (scheduled task) restarts the daemon from the new exe;
    5. check /api/health (commit) on every -HealthUrls; on failure put the previous exes back (automatic rollback);
    6. relaunch the desktop window if one was open before.
  Nothing is machine-specific: pass the paths and URLs, or set TMXG_HOME / TMXG_PORT. No secrets are read or printed.
  Run it DETACHED from an agent's shell (Start-Process -WindowStyle Hidden powershell -File ...): the swap stops the
  daemon the agent lives under.
.PARAMETER Sha          Commit, tag or ref to deploy (default origin/fleet/main).
.PARAMETER Repo         Build checkout of blackcode-ch/tmx-go (detached HEAD is moved; keep it a dedicated checkout).
.PARAMETER InstallDir   Folder with tmx-go.exe and Tiamatrix.exe (default %LOCALAPPDATA%\Programs\Tiamatrix).
.PARAMETER TmxHome      The daemon's home (default $env:TMXG_HOME). Never touched except the optional start gate.
.PARAMETER Port         Local daemon port (default $env:TMXG_PORT or 8797).
.PARAMETER HealthUrls   Extra base URLs to check after the swap (e.g. the tailnet addresses).
.PARAMETER StartGate    Optional start script holding a line  $expectedSha = '<64 hex>'  that is updated with the new exe's hash.
.PARAMETER GoExe        go.exe to build with (default: go on PATH).
.PARAMETER LogPath      Log file (default update-machine.log next to this script).
.PARAMETER NoSwap       Build and test only; stop before the swap.
.PARAMETER SkipTests    Skip the tests (repeat runs only).
.PARAMETER Force        Asserts "I have confirmed no recording is running" (the check needs the owner token, which an agent may not use). Required for the swap; use it only inside an announced window or after checking the recorder's own status.
#>
[CmdletBinding()]
param(
    [string]$Sha = 'origin/fleet/main',
    [Parameter(Mandatory = $true)][string]$Repo,
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\Tiamatrix'),
    [string]$TmxHome = $env:TMXG_HOME,
    [int]$Port = $(if ($env:TMXG_PORT) { [int]$env:TMXG_PORT } else { 8797 }),
    [string[]]$HealthUrls = @(),
    [string]$StartGate = '',
    [string]$GoExe = 'go',
    [string[]]$TestPackages = @('./internal/api/', './internal/ownerpin/', './internal/peers/', './internal/fleet/', './internal/p2p/', './internal/chat/'),
    [int]$WarnSeconds = 60,
    [int]$OwnerActiveMinutes = 5,
    [string]$LogPath = (Join-Path $PSScriptRoot 'update-machine.log'),
    [switch]$NoSwap, [switch]$SkipTests, [switch]$Force
)
$ErrorActionPreference = 'Stop'
$base = "http://127.0.0.1:$Port"
$daemonExe = Join-Path $InstallDir 'tmx-go.exe'
$windowExe = Join-Path $InstallDir 'Tiamatrix.exe'

function Log([string]$m) { $l = '{0:yyyy-MM-dd HH:mm:ss}  {1}' -f (Get-Date), $m; Add-Content -LiteralPath $LogPath -Value $l -Encoding UTF8; $l }
function Run([string]$what, [scriptblock]$b) {
    Log "START $what"
    $ErrorActionPreference = 'Continue'            # native stderr (git, go) is not an error by itself
    $o = & $b 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) { Log ("FAIL $what (exit $LASTEXITCODE): " + (($o -replace '\s+', ' ').Trim())); throw "$what failed" }
    Log "OK $what"; $o
}
function Health([string]$url) { try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "$url/api/health").Content | ConvertFrom-Json } catch { $null } }
function WaitHealthy([int]$secs) { for ($i = 0; $i -lt $secs; $i++) { Start-Sleep 1; $h = Health $base; if ($h -and $h.ok) { return $h } }; $null }
function SetGate([string]$sha) {
    if (-not $StartGate) { return }
    $t = Get-Content -LiteralPath $StartGate -Raw
    $t2 = [regex]::Replace($t, "(?m)^\`$expectedSha = '[0-9A-Fa-f]{64}'", "`$expectedSha = '$sha'")
    if ($t2 -eq $t -and $t -notmatch [regex]::Escape("'$sha'")) { throw "no `$expectedSha line in $StartGate" }
    Set-Content -LiteralPath $StartGate -Value $t2 -NoNewline -Encoding UTF8
}
# Seconds since the last keyboard/mouse input of the logged-on user (own session); [double]::MaxValue when unknown.
function OwnerIdleSeconds {
    try {
        Add-Type -Namespace TmxUpd -Name Idle -MemberDefinition @'
[StructLayout(LayoutKind.Sequential)] public struct LASTINPUTINFO { public uint cbSize; public uint dwTime; }
[DllImport("user32.dll")] public static extern bool GetLastInputInfo(ref LASTINPUTINFO p);
'@ -ErrorAction SilentlyContinue
        $i = New-Object TmxUpd.Idle+LASTINPUTINFO; $i.cbSize = [uint32][Runtime.InteropServices.Marshal]::SizeOf($i)
        if (-not [TmxUpd.Idle]::GetLastInputInfo([ref]$i)) { return [double]::MaxValue }
        return ([Environment]::TickCount - [int]$i.dwTime) / 1000.0
    } catch { return [double]::MaxValue }
}
# A message box that closes itself, shown on the owner's desktop without blocking this script.
function ShowOnScreen([string]$text, [int]$secs) {
    $cmd = "(New-Object -ComObject WScript.Shell).Popup('$($text -replace "'", "''")', $secs, 'Tiamatrix update', 64) | Out-Null"
    Start-Process -WindowStyle Hidden -FilePath 'powershell' -ArgumentList '-NoProfile', '-Command', $cmd
}

try {
    Log "=== update-machine start ($Sha) on $env:COMPUTERNAME"
    $free = [math]::Round((Get-PSDrive -Name ((Split-Path -Qualifier $Repo).TrimEnd(':'))).Free / 1GB); Log "disk free: $free GB"
    if ($free -lt 20) { throw 'less than 20 GB free: not building' }
    $env:GOFLAGS = '-p=2'; $env:CGO_ENABLED = '0'
    Push-Location $Repo
    Run 'git fetch' { git fetch origin } | Out-Null
    $full = (git rev-parse "$Sha^{commit}").Trim(); $short = $full.Substring(0, 7)
    Run "checkout $short" { git checkout --detach $full } | Out-Null
    $out = Join-Path $Repo "build\update-$short"; New-Item -ItemType Directory -Force $out | Out-Null
    Push-Location (Join-Path $Repo 'ui')
    Run 'ui install' { corepack pnpm install --frozen-lockfile } | Out-Null
    Run 'ui build' { corepack pnpm build } | Out-Null
    Pop-Location
    $ver = "-X github.com/blackcode-ch/tmx-go/internal/config.Version=$short"
    Run 'go build daemon' { & $GoExe build -trimpath -ldflags "-s -w $ver" -o (Join-Path $out 'tmx-go.exe') . } | Out-Null
    Run 'go build window' { & $GoExe build -trimpath -tags 'desktop,production' -ldflags $ver -o (Join-Path $out 'Tiamatrix.exe') . } | Out-Null
    if (-not $SkipTests) {
        $t = Run 'tests' { & $GoExe test -p 2 @TestPackages }
        Log (($t.Trim()) -replace '\r?\n', ' | ')
    }
    Pop-Location
    if ($NoSwap) { Log "built $short in $out; -NoSwap: stopping before the swap"; exit 0 }

    # ---- swap -------------------------------------------------------------------------------------------------
    $before = Health $base
    if (-not $before) { throw 'daemon is not healthy now; fix that first (no upgrade on top of a broken daemon)' }
    $oldCommit = $before.commit.Substring(0, 7)
    # -Force = "I have confirmed no recording is running" (the check needs the owner token, which an agent may not use).
    if (-not $Force) { throw 'refusing to swap without -Force: confirm no recording is running (announced window / the recorder''s own status), then pass -Force' }
    $windowWasOpen = [bool](Get-Process -Name 'Tiamatrix' -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $windowExe })

    if ((OwnerIdleSeconds) -lt ($OwnerActiveMinutes * 60)) {
        Log "owner is active at the machine: warning on screen, restart in $WarnSeconds s"
        ShowOnScreen "Tiamatrix updates to $short in $WarnSeconds seconds. The window closes and reopens by itself (about a minute)." $WarnSeconds
        Start-Sleep -Seconds $WarnSeconds
    }

    $oldSha = (Get-FileHash -LiteralPath $daemonExe).Hash
    $newSha = (Get-FileHash -LiteralPath (Join-Path $out 'tmx-go.exe')).Hash
    Copy-Item -LiteralPath $daemonExe -Destination (Join-Path $InstallDir "tmx-go.prev-$oldCommit.exe") -Force
    Copy-Item -LiteralPath $windowExe -Destination (Join-Path $InstallDir "Tiamatrix.prev-$oldCommit.exe") -Force
    Log "backup: *.prev-$oldCommit.exe"
    $t0 = Get-Date
    $ok = $false; $h = $null; $running = $null
    # From here on ANY failure (a locked file, a null path, the gate write) must end in the rollback below.
    try {
        Get-Process -Name 'Tiamatrix' -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $windowExe } | ForEach-Object {
            $_.CloseMainWindow() | Out-Null; Start-Sleep 2; if (-not $_.HasExited) { Stop-Process -Id $_.Id -Force } }
        $env:TMXG_HOME = $TmxHome; $env:TMXG_PORT = "$Port"
        & $daemonExe down | Out-Null                       # the old exe stops its own daemon
        for ($i = 0; $i -lt 20 -and (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue); $i++) { Start-Sleep 1 }
        # The watchdog may restart the OLD exe in this gap: swap immediately.
        Copy-Item -LiteralPath (Join-Path $out 'tmx-go.exe') -Destination $daemonExe -Force
        Copy-Item -LiteralPath (Join-Path $out 'Tiamatrix.exe') -Destination $windowExe -Force
        SetGate $newSha
        Log "swapped to $short"

        $h = WaitHealthy 90
        # Only THIS install's daemon counts (a second tmx-go.exe elsewhere on the machine must not matter).
        $running = @(Get-CimInstance Win32_Process -Filter "Name='tmx-go.exe'" |
            Where-Object { $_.CommandLine -match ' daemon' -and $_.ExecutablePath -and ($_.ExecutablePath -eq $daemonExe) })
        $runSha = if ($running.Count -gt 0) { (Get-FileHash -LiteralPath $running[0].ExecutablePath).Hash } else { '' }
        $ok = [bool]($h -and $runSha -eq $newSha -and $h.commit -eq $full)
        if ($ok) {
            foreach ($u in $HealthUrls) {
                $x = Health $u.TrimEnd('/')
                if ($x -and $x.commit -eq $full) { Log "CHECK $u -> commit $short" } else { Log "CHECK $u FAILED"; $ok = $false }
            }
        }
    } catch { Log "swap/verify error: $($_.Exception.Message)"; $ok = $false }
    if ($ok) {
        Log ('UPDATE OK in {0:N0}s: {1}, PID {2}' -f ((Get-Date) - $t0).TotalSeconds, $short, $running[0].ProcessId)
        Log ('(old daemon sha ' + $oldSha.Substring(0, 12) + ', new ' + $newSha.Substring(0, 12) + ')')
        if ($windowWasOpen) { Start-Process -FilePath $windowExe; Log 'window relaunched (it was open before)' }
        Log '=== update-machine end OK'; exit 0
    }

    # ---- automatic rollback ------------------------------------------------------------------------------------
    Log "UPDATE FAILED (health=$([bool]$h)): rolling back to $oldCommit"
    try {
        & $daemonExe down 2>$null | Out-Null
        for ($i = 0; $i -lt 20 -and (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue); $i++) { Start-Sleep 1 }
        Copy-Item -LiteralPath (Join-Path $InstallDir "tmx-go.prev-$oldCommit.exe") -Destination $daemonExe -Force
        Copy-Item -LiteralPath (Join-Path $InstallDir "Tiamatrix.prev-$oldCommit.exe") -Destination $windowExe -Force
        SetGate $oldSha
        $h = WaitHealthy 90
        if ($h -and $h.commit.Substring(0, 7) -eq $oldCommit) { Log "ROLLED BACK: commit $($h.commit) healthy"; if ($windowWasOpen) { Start-Process -FilePath $windowExe } }
        elseif ($h) { Log "ROLLBACK: daemon healthy but on commit $($h.commit), not $oldCommit; check by hand" }
        else { Log 'ROLLBACK: daemon not healthy yet; the watchdog keeps trying. See the daemon log in the tmx home.' }
    } catch { Log "ROLLBACK FAILED: $($_.Exception.Message); restore the *.prev-$oldCommit.exe files by hand" }
    exit 1
} catch { Log "=== update-machine FAILED: $($_.Exception.Message)"; exit 1 }
