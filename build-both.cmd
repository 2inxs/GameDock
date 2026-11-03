@echo off
setlocal
cd /d "%~dp0"
pushd frontend
call npm ci
if errorlevel 1 exit /b 1
call npm run build
if errorlevel 1 exit /b 1
popd
go build -tags desktop,production -ldflags=-H=windowsgui -o build/bin/GameDock-0.2.exe .
if errorlevel 1 exit /b 1
go build -tags client,desktop,production -ldflags=-H=windowsgui -o build/bin/GameDock-Connect.exe .
if errorlevel 1 exit /b 1
