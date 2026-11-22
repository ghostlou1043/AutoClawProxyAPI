@echo off
chcp 65001 >nul
setlocal
title AutoClaw2API 本地调试启动器 (by VILK)
cd /d "%~dp0"

echo.
echo [AutoClaw2API] 正在检查 Go 运行环境...
where go >nul 2>&1
if errorlevel 1 goto no_go

echo [AutoClaw2API] 正在启动本地调试服务...
echo [AutoClaw2API] 控制台地址: http://localhost:8080 (具体端口以 config.json 为准)
echo [AutoClaw2API] 按 Ctrl+C 可优雅停止服务
echo.

go run ./cmd/autoclaw2api -config config.json
goto done

:no_go
echo [错误] 未检测到 Go 编译器，请先安装 Go 1.24+ 并配置 PATH。

:done
echo.
echo [AutoClaw2API] 服务已退出，按任意键关闭窗口...
pause >nul
