#!/bin/bash
# Build the Android client APK from the workspace-local SDK.
# Requires .runtime/android-sdk, .runtime/bin/wails3, and a JDK 21.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPO="$(cd "$PROJECT/../.." && pwd)"
SDK="${ANDROID_HOME:-$REPO/.runtime/android-sdk}"
NDK="${ANDROID_NDK_HOME:-$(ls -d "$SDK"/ndk/* 2>/dev/null | sort -V | tail -1)}"
JAVA_HOME="${JAVA_HOME:-$(/usr/libexec/java_home -v 21 2>/dev/null || true)}"
WAILS3="${WAILS3:-$REPO/.runtime/bin/wails3}"

if [ ! -x "$WAILS3" ]; then
  echo "wails3 not found at $WAILS3" >&2
  exit 1
fi
if [ -z "${NDK:-}" ] || [ ! -d "$NDK" ]; then
  echo "Android NDK not found under $SDK/ndk" >&2
  exit 1
fi

export ANDROID_HOME="$SDK" ANDROID_SDK_ROOT="$SDK" ANDROID_NDK_HOME="$NDK"
export ANDROID_USER_HOME="$REPO/.runtime/android-user" ANDROID_AVD_HOME="$REPO/.runtime/android-avd"
export HOME="$REPO/.runtime/android-home" GRADLE_USER_HOME="$REPO/.runtime/gradle-home"
export GOMODCACHE="$REPO/.runtime/mod" GOCACHE="$REPO/.runtime/gocache" GOPATH="$REPO/.runtime/gopath"
export GOTOOLCHAIN=auto JAVA_HOME
mkdir -p "$HOME" "$ANDROID_USER_HOME" "$GRADLE_USER_HOME"

TOOLCHAIN="$NDK/toolchains/llvm/prebuilt/darwin-x86_64"
if [ ! -d "$TOOLCHAIN" ]; then
  TOOLCHAIN="$NDK/toolchains/llvm/prebuilt/darwin-arm64"
fi
export CC="$TOOLCHAIN/bin/aarch64-linux-android21-clang"
export CXX="$TOOLCHAIN/bin/aarch64-linux-android21-clang++"
export CGO_ENABLED=1 GOOS=android GOARCH=arm64

cd "$PROJECT"
"$WAILS3" android overlay:gen -out build/android/overlay.json -config build/config.yml >/dev/null
mkdir -p build/android/app/src/main/jniLibs/arm64-v8a
go build -buildmode=c-shared -overlay build/android/overlay.json \
  -tags android,debug -buildvcs=false -ldflags="-s -w" \
  -o build/android/app/src/main/jniLibs/arm64-v8a/libwails.so .

cd build/android
chmod +x ./gradlew
./gradlew assembleDebug --console=plain
mkdir -p "$PROJECT/bin"
cp app/build/outputs/apk/debug/app-debug.apk "$PROJECT/bin/Meerkat-Android-debug.apk"
echo "APK: $PROJECT/bin/Meerkat-Android-debug.apk"
