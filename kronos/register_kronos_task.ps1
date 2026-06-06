# register_kronos_task.ps1 — One-time setup: registers Kronos as a Windows Scheduled Task.
# Run this script ONCE as Administrator.
#
# Usage:
#   Right-click PowerShell → "Run as administrator"
#   cd C:\Projects\bnf_go_engine\kronos
#   .\register_kronos_task.ps1

$TaskName   = "KronosAIService"
$ScriptDir  = Split-Path -Parent $MyInvocation.MyCommand.Path
$WrapperPs1 = Join-Path $ScriptDir "start_kronos.ps1"

# Remove old task if it exists
Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue

# Build the action: run PowerShell with the wrapper script
$Action = New-ScheduledTaskAction `
    -Execute "powershell.exe" `
    -Argument "-NonInteractive -ExecutionPolicy Bypass -File `"$WrapperPs1`"" `
    -WorkingDirectory $ScriptDir

# Trigger: at system startup (runs even before user login)
$Trigger = New-ScheduledTaskTrigger -AtStartup

# Settings: restart on failure (up to 3 rapid failures, then 1-minute cool-down)
$Settings = New-ScheduledTaskSettingsSet `
    -ExecutionTimeLimit (New-TimeSpan -Hours 0) `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -StartWhenAvailable `
    -RunOnlyIfNetworkAvailable:$false

# Run as SYSTEM so it works without a logged-in user
$Principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -RunLevel Highest

Register-ScheduledTask `
    -TaskName   $TaskName `
    -Action     $Action `
    -Trigger    $Trigger `
    -Settings   $Settings `
    -Principal  $Principal `
    -Description "Kronos AI microservice (FastAPI on port 8765). Auto-restarts on crash."

Write-Host ""
Write-Host "Task '$TaskName' registered successfully."
Write-Host "The Kronos service will start automatically at next boot."
Write-Host ""
Write-Host "Manual controls:"
Write-Host "  Start now : Start-ScheduledTask  -TaskName '$TaskName'"
Write-Host "  Stop      : Stop-ScheduledTask   -TaskName '$TaskName'"
Write-Host "  Status    : Get-ScheduledTask    -TaskName '$TaskName' | Select State"
Write-Host "  Remove    : Unregister-ScheduledTask -TaskName '$TaskName' -Confirm:`$false"
