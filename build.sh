#!/usr/bin/env bash
# Build Media Tools into a standalone executable.
set -euo pipefail

echo "========================================"
echo " Media Tools - build"
echo "========================================"
echo

if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: Go is not installed or not on PATH." >&2
  echo "Install Go 1.21 or newer from https://go.dev/dl/" >&2
  exit 1
fi

echo "[1/4] Go toolchain"
go version

echo
echo "[2/4] Checking for the Wails CLI..."
if ! command -v wails >/dev/null 2>&1; then
  echo "Wails CLI not found. Installing..."
  go install github.com/wailsapp/wails/v2/cmd/wails@latest
  export PATH="$PATH:$(go env GOPATH)/bin"
fi
wails version || true

echo
echo "[3/4] Running tests..."
go test . ./internal/...

echo
echo "[4/4] Building executable..."
wails build -clean

echo
echo "========================================"
echo " SUCCESS"
echo "========================================"
echo
echo "  build/bin/MediaTools"
echo
echo "Runtime requirements:"
echo "  - ffmpeg and ffprobe on PATH  (https://ffmpeg.org/download.html)"
echo "  - yt-dlp on PATH, for the download tools  (pip install yt-dlp)"
echo
echo "Hardware encoding is detected automatically at startup; the CPU"
echo "encoders always remain available as a fallback."
echo
