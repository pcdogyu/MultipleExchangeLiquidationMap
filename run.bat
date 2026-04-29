@echo off
setlocal

set "DEBUG=1"
set "DEBUG_LOG=server.log"

go test ./...
if errorlevel 1 exit /b %errorlevel%

go build .
if errorlevel 1 exit /b %errorlevel%

go run .
