@echo off
setlocal

echo [1/3] go test ./...
go test ./...
if errorlevel 1 goto fail

echo [2/3] go build .
go build .
if errorlevel 1 goto fail

if not exist log mkdir log

echo [3/3] DEBUG=1 DEBUG_LOG=log/server.log go run .
set DEBUG=1
set DEBUG_LOG=log/server.log
go run .
if errorlevel 1 goto fail

exit /b 0

:fail
echo.
echo run.bat failed.
exit /b 1
