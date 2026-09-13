#!/usr/bin/env bash
# DeepSentry v2.0.4 一键交叉编译

set -euo pipefail

APP_NAME="deepsentry"
OUTPUT_DIR="build"
APP_VERSION="$(tr -d '[:space:]' < VERSION)"
BUILD_TIME="$(date '+%Y-%m-%d')"
LDFLAGS="-s -w -X 'ai-edr/internal/ui.Version=${APP_VERSION}' -X 'ai-edr/internal/ui.BuildTime=${BUILD_TIME}'"

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

STAGING="$(mktemp -d "${TMPDIR:-/tmp}/deepsentry-build.XXXXXX")"
trap 'rm -rf "$STAGING"' EXIT
mkdir -p "$STAGING/bin" "$OUTPUT_DIR/bin"

build_main() {
  local goos=$1 goarch=$2
  local out="$STAGING/${APP_NAME}-${goos}-${goarch}"
  [[ "$goos" == "windows" ]] && out="${out}.exe"

  echo "🔨 $goos/$goarch → $(basename "$out")"
  # Build the package, not a hand-maintained list of files. Go will select
  # console_windows.go/console_other.go through build tags, new entry files
  # cannot be silently omitted, and module metadata stays attributable.
  env CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
    go build -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o "$out" ./cmd
}

build_aux() {
  local name=$1 pkg=$2
  local goos=$3 goarch=$4
  local out="$STAGING/bin/${name}-${goos}-${goarch}"
  [[ "$goos" == "windows" ]] && out="${out}.exe"
  env CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
    go build -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o "$out" "$pkg"
}

platforms=(
  "darwin/amd64"
  "darwin/arm64"
  "linux/amd64"
  "linux/arm64"
  "linux/386"
  "windows/amd64"
  "windows/386"
)

echo "🏷  版本: v${APP_VERSION}  ·  Build Time: ${BUILD_TIME}"
echo "🚀 开始编译..."
echo "------------------------------------------"
for p in "${platforms[@]}"; do
  IFS=/ read -r goos goarch <<< "$p"
  build_main "$goos" "$goarch"
done

# 当前主机架构的 benchmark / smoke（便于本地评测）
HOST_OS=$(go env GOOS)
HOST_ARCH=$(go env GOARCH)
build_aux benchmark ./cmd/benchmark/main.go "$HOST_OS" "$HOST_ARCH"
build_aux smoke ./cmd/smoke/main.go "$HOST_OS" "$HOST_ARCH"
build_aux toolsmoke ./cmd/toolsmoke/main.go "$HOST_OS" "$HOST_ARCH"

# 只替换构建脚本拥有的已知产物；build/ 下的配置副本、报告、ZIP 和用户文件不删除。
# 不能使用 deepsentry-*-* 通配符，它会误删发布包
# deepsentry-v<version>-all-platforms.zip。
for p in "${platforms[@]}"; do
  IFS=/ read -r goos goarch <<< "$p"
  artifact="${APP_NAME}-${goos}-${goarch}"
  [[ "$goos" == "windows" ]] && artifact="${artifact}.exe"
  rm -f "$OUTPUT_DIR/$artifact"
done
rm -f "$OUTPUT_DIR/deepsentry" "$OUTPUT_DIR/deepsentry.exe"
find "$OUTPUT_DIR/bin" -maxdepth 1 -type f \( -name 'deepsentry' -o -name 'deepsentry.exe' -o -name 'benchmark-*' -o -name 'smoke-*' -o -name 'toolsmoke-*' \) -delete
cp "$STAGING"/deepsentry-* "$OUTPUT_DIR/"
cp "$STAGING"/bin/* "$OUTPUT_DIR/bin/"

HOST_BINARY="$STAGING/${APP_NAME}-${HOST_OS}-${HOST_ARCH}"
HOST_ALIAS="$OUTPUT_DIR/bin/deepsentry"
ROOT_ALIAS="$ROOT/deepsentry"
if [[ "$HOST_OS" == "windows" ]]; then
  HOST_BINARY="${HOST_BINARY}.exe"
  HOST_ALIAS="${HOST_ALIAS}.exe"
  ROOT_ALIAS="${ROOT_ALIAS}.exe"
fi
cp "$HOST_BINARY" "$HOST_ALIAS"
cp "$HOST_BINARY" "$OUTPUT_DIR/$(basename "$HOST_ALIAS")"
cp "$HOST_BINARY" "$ROOT_ALIAS"

# 配置与环境文件可能含密钥。既不删除也不打包，只收紧现有文件权限。
find "$OUTPUT_DIR" -maxdepth 1 -type f \( -iname 'config*.yaml' -o -name '.env' \) -exec chmod 600 {} +

# 公开校验清单必须与 GitHub Release/ZIP 的 7 个跨平台主程序完全一致。
# 不列出本机别名和 bin/ 下的内部 benchmark/smoke，否则用户下载 Release
# 后执行 `shasum -c SHA256SUMS` 会因缺少非发布文件而误报失败。
release_artifacts=()
for p in "${platforms[@]}"; do
  IFS=/ read -r goos goarch <<< "$p"
  artifact="${APP_NAME}-${goos}-${goarch}"
  [[ "$goos" == "windows" ]] && artifact="${artifact}.exe"
  release_artifacts+=("$artifact")
done
(
  cd "$OUTPUT_DIR"
  printf './%s\0' "${release_artifacts[@]}" | sort -z | xargs -0 shasum -a 256
) > "$OUTPUT_DIR/SHA256SUMS"

release_zip="$OUTPUT_DIR/${APP_NAME}-v${APP_VERSION}-all-platforms.zip"
rm -f "$release_zip"
(
  cd "$OUTPUT_DIR"
  zip -q "$(basename "$release_zip")" "${release_artifacts[@]}"
)

echo "------------------------------------------"
echo "✅ 编译完成"
echo "💡 运行: cd build && ./deepsentry -c config.yaml"
echo "   或:   ./build/bin/deepsentry -c build/config.yaml"
ls -lh "$OUTPUT_DIR"
ls -lh "$OUTPUT_DIR/bin" 2>/dev/null || true
