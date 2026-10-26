#!/usr/bin/env bash
# 构建 LXCPanel：交叉编译 linux amd64/arm64 -> 同步前端 -> 打包 fpk
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/src"
APP="$ROOT/LXCPanel"

# locate go
if [ -x "$ROOT/tools/go/bin/go.exe" ]; then
  GO="$ROOT/tools/go/bin/go.exe"
elif [ -x "$ROOT/tools/go/bin/go" ]; then
  GO="$ROOT/tools/go/bin/go"
else
  GO="$(command -v go)"
fi

echo "==> go: $GO"
"$GO" version

mkdir -p "$APP/app/server" "$APP/app/www"

# 版本号默认取 manifest 里写的版本，避免忘记传 VERSION 打出 1.0.0 的包。
BUILD_VERSION="${VERSION:-$(grep -E '^version[[:space:]]*=' "$APP/manifest" | head -1 | sed 's/.*=[[:space:]]*//' | tr -d ' \r')}"
if [ -z "$BUILD_VERSION" ]; then
  echo "!! 无法从 manifest 读取版本号" >&2
  exit 1
fi
export VERSION="$BUILD_VERSION"
echo "==> version: $BUILD_VERSION"

cd "$SRC"

build_one() {
  local arch="$1"
  local out="$APP/app/server/lxcpanel-$arch"
  echo "==> building linux/$arch"
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 "$GO" build \
    -trimpath -ldflags "-s -w -X main.appVersion=${VERSION}" \
    -o "$out" .
  ls -lh "$out" | awk '{print "    "$5" "$9}'
}

build_one amd64
build_one arm64

echo "==> syncing web assets"
rm -rf "$APP/app/www"
mkdir -p "$APP/app/www"
cp -r "$SRC/www/." "$APP/app/www/"

# strip macOS junk if present
find "$APP" -name '.DS_Store' -delete 2>/dev/null || true

echo "==> packaging fpk"
cd "$APP"
"$ROOT/fnpack.exe" build "$@"

FPK="$APP/LXCPanel.fpk"
if [ -f "$FPK" ]; then
  echo "==> normalizing file modes"
  PY=""
  if [ -x "$ROOT/tools/py/python.exe" ]; then
    PY="$ROOT/tools/py/python.exe"
  elif command -v python3 >/dev/null 2>&1; then
    PY="python3"
  elif command -v python >/dev/null 2>&1; then
    PY="python"
  fi
  if [ -n "$PY" ]; then
    "$PY" "$ROOT/tools/fix_fpk_modes.py" "$FPK"
  else
    echo "    !! python not found, skip mode fix"
  fi
fi

echo "==> done"
ls -lh "$FPK" 2>/dev/null || true
