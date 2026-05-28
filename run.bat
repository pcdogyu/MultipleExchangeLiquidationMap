@echo off
setlocal

if /I "%~1"=="prune" goto prune

set "DEBUG=1"
set "DEBUG_LOG=log/server.log"
set "APP_PORT=80"
set "DB_PATH=data\liqmap.db"
set "DB_FILE="
set "DB_DIR="
set "TELEGRAM_PHOTO_TIMEOUT_SEC=120"
set "EXE=multipleexchangeliquidationmap.exe"
set "NEW_EXE=multipleexchangeliquidationmap.new.exe"
rem If this host cannot reach api.telegram.org directly, point this to a private Bot API or reverse proxy.
rem set "TELEGRAM_API_BASE_URL=https://your-telegram-bot-api.example.com"
rem Set BUILD=1 to run tests before launching.

if /I "%~1"=="_after_pull" goto afterPull

call :pullLatestCode
echo Reloading run.bat after git pull...
call "%~f0" _after_pull
exit /b %errorlevel%

:afterPull
call :loadVersionInfo
call :resolvePaths

echo Checking port %APP_PORT%...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$port = [int]$env:APP_PORT; $procIds = @(Get-NetTCPConnection -LocalPort $port -ErrorAction SilentlyContinue | Select-Object -ExpandProperty OwningProcess -Unique | Where-Object { $_ -gt 0 }); if ($procIds.Count -eq 0) { Write-Host ('No process is using port {0}' -f $port) }; foreach ($procId in $procIds) { try { $proc = Get-Process -Id $procId -ErrorAction Stop; Write-Host ('Stopping port {0} owner PID {1} {2}' -f $port, $procId, $proc.ProcessName); Stop-Process -Id $procId -Force -ErrorAction Stop } catch { Write-Host ('Failed to stop PID {0}: {1}' -f $procId, $_.Exception.Message) } }"

where go >nul 2>nul
if errorlevel 1 (
  if exist "%EXE%" (
    echo Go was not found in PATH; using existing %EXE%.
    goto start
  )
  echo Go was not found in PATH and %EXE% does not exist.
  echo Install Go 1.21+ or place a prebuilt %EXE% in this directory, then run this script again.
  goto fail
)

if /I "%BUILD%"=="1" (
  echo [1/4] go test ./...
  go test ./...
  if errorlevel 1 goto fail
)

if exist "%NEW_EXE%" (
  del /f /q "%NEW_EXE%"
  if errorlevel 1 goto fail
)

echo [2/4] go build -o %NEW_EXE% .
go build -o "%NEW_EXE%" .
if errorlevel 1 goto fail

echo [3/4] Replacing %EXE%...
if exist "%EXE%" (
  del /f /q "%EXE%"
  if errorlevel 1 goto fail
)
move /y "%NEW_EXE%" "%EXE%" >nul
if errorlevel 1 goto fail

:start
if not exist log mkdir log

echo Database:
echo   DB path: %DB_PATH%
echo   DB file: %DB_FILE%
echo   DB dir: %DB_DIR%
echo Version:
echo   Branch: %VERSION_BRANCH%
echo   Commit short: %VERSION_COMMIT%
echo   Commit hash: %VERSION_COMMIT_FULL%
echo   Commit time: %VERSION_COMMIT_TIME%
echo [4/4] Starting in debug mode: DEBUG=%DEBUG% DEBUG_LOG=%DEBUG_LOG%
"%EXE%"
if errorlevel 1 goto fail

exit /b 0

:pullLatestCode
where git >nul 2>nul
if errorlevel 1 (
  echo Git was not found in PATH; cannot pull latest code.
  goto fail
)
echo [0/4] Pulling latest code from origin/golangv2...
git pull --ff-only origin golangv2
if errorlevel 1 goto fail
exit /b 0

:loadVersionInfo
set "VERSION_BRANCH="
set "VERSION_COMMIT="
set "VERSION_COMMIT_FULL="
set "VERSION_COMMIT_TIME="
where git >nul 2>nul
if errorlevel 1 goto versionFallback
for /f "usebackq delims=" %%A in (`git rev-parse --abbrev-ref HEAD 2^>nul`) do set "VERSION_BRANCH=%%A"
for /f "usebackq delims=" %%A in (`git rev-parse --short HEAD 2^>nul`) do set "VERSION_COMMIT=%%A"
for /f "usebackq delims=" %%A in (`git rev-parse HEAD 2^>nul`) do set "VERSION_COMMIT_FULL=%%A"
for /f "usebackq delims=" %%A in (`git show -s --format^=%%ci HEAD 2^>nul`) do set "VERSION_COMMIT_TIME=%%A"
:versionFallback
if "%VERSION_BRANCH%"=="" set "VERSION_BRANCH=-"
if "%VERSION_COMMIT%"=="" set "VERSION_COMMIT=-"
if "%VERSION_COMMIT_FULL%"=="" set "VERSION_COMMIT_FULL=-"
if "%VERSION_COMMIT_TIME%"=="" set "VERSION_COMMIT_TIME=-"
exit /b 0

:resolvePaths
for %%I in ("%DB_PATH%") do set "DB_FILE=%%~fI"
for %%I in ("%DB_FILE%") do set "DB_DIR=%%~dpI"
exit /b 0

:prune
set "PRUNE_SCRIPT=%~dp0data\prune-liqmap-retention.ps1"
if not exist "%PRUNE_SCRIPT%" (
  echo Prune script not found: %PRUNE_SCRIPT%
  goto fail
)
echo Running database prune script...
powershell -NoProfile -ExecutionPolicy Bypass -File "%PRUNE_SCRIPT%"
if errorlevel 1 goto fail
exit /b 0

:fail
if exist "%NEW_EXE%" del /f /q "%NEW_EXE%" >nul 2>nul
echo.
echo run.bat failed.
exit /b 1
