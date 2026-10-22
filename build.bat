@echo off
REM Build Media Tools into a single standalone Windows executable.
setlocal

echo ========================================
echo  Media Tools - build
echo ========================================
echo.

where go >nul 2>&1
if errorlevel 1 (
    echo ERROR: Go is not installed or not on PATH.
    echo Install Go 1.21 or newer from https://go.dev/dl/
    pause
    exit /b 1
)

echo [1/4] Go toolchain
go version

echo.
echo [2/4] Checking for the Wails CLI...
where wails >nul 2>&1
if errorlevel 1 (
    echo Wails CLI not found. Installing...
    go install github.com/wailsapp/wails/v2/cmd/wails@latest
    if errorlevel 1 (
        echo ERROR: could not install the Wails CLI.
        pause
        exit /b 1
    )
    echo NOTE: if 'wails' is still not found, add %%USERPROFILE%%\go\bin to PATH.
)

echo.
echo [3/4] Running tests...
go test . ./internal/...
if errorlevel 1 (
    echo ERROR: tests failed. Build aborted.
    pause
    exit /b 1
)

echo.
echo [4/4] Building executable...
wails build -clean
if errorlevel 1 (
    echo ERROR: build failed.
    pause
    exit /b 1
)

echo.
echo ========================================
echo  SUCCESS
echo ========================================
echo.
echo   build\bin\MediaTools.exe
echo.
echo Runtime requirements:
echo   - ffmpeg and ffprobe on PATH  (https://ffmpeg.org/download.html)
echo   - yt-dlp on PATH, for the download tools  (pip install yt-dlp)
echo   - WebView2 runtime (preinstalled on Windows 10 21H2 and later)
echo.
echo Hardware encoding is detected automatically at startup; no NVIDIA card
echo is required, and the CPU encoders always remain available.
echo.
pause
endlocal
