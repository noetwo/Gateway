@echo off
REM 在运行前请把下面的 GO_ROOT 改成你本机 Go 安装路径，或者把 go.exe 加进 PATH 后直接删掉前三行。
set GOROOT=C:\path\to\go
set GOPATH=C:\path\to\gopath
set PATH=%GOROOT%\bin;%PATH%
cd /d %~dp0\..
go build -o main_new.exe .
echo BUILD_EXIT=%ERRORLEVEL%
