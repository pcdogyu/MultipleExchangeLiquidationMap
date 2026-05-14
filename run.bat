@echo off
setlocal

set "DEBUG=1"
set "DEBUG_LOG=log/server.log"
set "APP_PORT=80"
set "TELEGRAM_PHOTO_TIMEOUT_SEC=120"
rem If this host cannot reach api.telegram.org directly, point this to a private Bot API or reverse proxy.
rem set "TELEGRAM_API_BASE_URL=https://your-telegram-bot-api.example.com"
rem Set BUILD=1 to run tests before launching.

echo Checking port %APP_PORT%...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$port = [int]$env:APP_PORT; $procIds = @(Get-NetTCPConnection -LocalPort $port -ErrorAction SilentlyContinue | Select-Object -ExpandProperty OwningProcess -Unique | Where-Object { $_ -gt 0 }); if ($procIds.Count -eq 0) { Write-Host ('No process is using port {0}' -f $port) }; foreach ($procId in $procIds) { try { $proc = Get-Process -Id $procId -ErrorAction Stop; Write-Host ('Stopping port {0} owner PID {1} {2}' -f $port, $procId, $proc.ProcessName); Stop-Process -Id $procId -Force -ErrorAction Stop } catch { Write-Host ('Failed to stop PID {0}: {1}' -f $procId, $_.Exception.Message) } }"

if /I "%BUILD%"=="1" (
  echo [1/4] go test ./...
  go test ./...
  if errorlevel 1 goto fail
)

echo [2/4] Removing old multipleexchangeliquidationmap.exe...
if exist multipleexchangeliquidationmap.exe (
  del /f /q multipleexchangeliquidationmap.exe
  if errorlevel 1 goto fail
)

echo [3/4] go build -o multipleexchangeliquidationmap.exe .
go build -o multipleexchangeliquidationmap.exe .
if errorlevel 1 goto fail

if not exist log mkdir log

echo [4/4] Starting in debug mode: DEBUG=%DEBUG% DEBUG_LOG=%DEBUG_LOG%
multipleexchangeliquidationmap.exe
if errorlevel 1 goto fail

exit /b 0

:fail
echo.
echo run.bat failed.
exit /b 1
