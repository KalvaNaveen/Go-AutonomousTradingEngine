# start_kronos.ps1 — Kronos AI service wrapper with auto-restart.
# Registered in Windows Task Scheduler to run at system startup.
# The loop restarts the service automatically if it exits for any reason.

$ScriptDir  = Split-Path -Parent $MyInvocation.MyCommand.Path
$ServicePy  = Join-Path $ScriptDir "service.py"
$LogFile    = Join-Path $ScriptDir "kronos_service.log"
$MaxRestarts = 99999   # effectively infinite
$RestartDelaySec = 10  # wait 10 s between restarts

$restarts = 0
while ($restarts -lt $MaxRestarts) {
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    Add-Content $LogFile "[$timestamp] Starting Kronos service (attempt $($restarts + 1))..."

    # Run python in unbuffered mode; redirect stderr to stdout so both go to the log
    $proc = Start-Process python -ArgumentList "-u `"$ServicePy`"" `
        -WorkingDirectory $ScriptDir `
        -RedirectStandardOutput $LogFile `
        -RedirectStandardError  "$LogFile.err" `
        -NoNewWindow -PassThru -Wait

    $exitCode = $proc.ExitCode
    $timestamp = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    Add-Content $LogFile "[$timestamp] Service exited with code $exitCode. Restarting in ${RestartDelaySec}s..."

    Start-Sleep -Seconds $RestartDelaySec
    $restarts++
}
