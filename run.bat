@echo off
setlocal

set "DEBUG=1"
set "DEBUG_LOG=server.log"
set "APP_PORT=8888"

echo Checking port %APP_PORT%...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$port = [int]$env:APP_PORT; $procIds = @(Get-NetTCPConnection -LocalPort $port -ErrorAction SilentlyContinue | Select-Object -ExpandProperty OwningProcess -Unique | Where-Object { $_ -gt 0 }); if ($procIds.Count -eq 0) { Write-Host ('No process is using port {0}' -f $port) }; foreach ($procId in $procIds) { try { $proc = Get-Process -Id $procId -ErrorAction Stop; Write-Host ('Stopping port {0} owner PID {1} {2}' -f $port, $procId, $proc.ProcessName); Stop-Process -Id $procId -Force -ErrorAction Stop } catch { Write-Host ('Failed to stop PID {0}: {1}' -f $procId, $_.Exception.Message) } }"

go test ./...
if errorlevel 1 exit /b %errorlevel%

go build .
if errorlevel 1 exit /b %errorlevel%

go run .
