@echo off
setlocal

set "DEBUG=1"
set "DEBUG_LOG=server.log"
set "APP_PORT=8888"

echo Checking port %APP_PORT%...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$procIds = @(Get-NetTCPConnection -LocalPort %APP_PORT% -ErrorAction SilentlyContinue | Select-Object -ExpandProperty OwningProcess -Unique | Where-Object { $_ -gt 0 }); foreach ($procId in $procIds) { try { $name = (Get-Process -Id $procId -ErrorAction SilentlyContinue).ProcessName; Write-Host ('Stopping port %APP_PORT% owner PID {0} {1}' -f $procId, $name); Stop-Process -Id $procId -Force -ErrorAction Stop } catch { Write-Host ('Failed to stop PID {0}: {1}' -f $procId, $_.Exception.Message) } }"

go test ./...
if errorlevel 1 exit /b %errorlevel%

go build .
if errorlevel 1 exit /b %errorlevel%

go run .
