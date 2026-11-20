# Meerkat 打包入口：macOS 宿主机上打 Windows 包与 macOS App。
#
# 常用：
#   make                      显示可用目标（同 make help）
#   make package-win          Windows 全量：Meerkat.exe + meerkat-edge.exe + zip + NSIS 安装包
#   make package-win-portable 只出免安装目录 + zip（没有 docker 也能用）
#   make package-macos        macOS Meerkat.app（调用 bundle.sh + ad-hoc 签名）
#   make package-all          Windows + macOS 全量
#   make clean                删除构建产物（保留 .runtime 构建缓存与用户配置）
#
# 可覆盖的环境变量：
#   DOCKER=...       docker 可执行文件；默认自动探测 PATH，再回退 ~/.orbstack/bin/docker
#   NSIS_IMAGE=...   NSIS 容器镜像；默认 meerkat-nsis:3.08
#   VERSION=...      覆盖根 VERSION，用于构建隔离测试版本。
#   UPDATE_PUBLIC_KEY=...  Ed25519 公钥，64 位十六进制。
#
# 约定：
# - 每条 recipe 以 `set -euo pipefail` 开头，任一步失败立即停止。
#   macOS 自带 GNU make 3.81 不支持 .ONESHELL，因此每个 recipe 写成单条 shell 命令。
# - Go 统一 GOTOOLCHAIN=auto；仓库存在 .runtime/gocache、.runtime/mod 时优先使用，可离线构建。
# - 产物全部位于 desktop/MeerkatDesktop/bin/（被 .gitignore 忽略，不入库）。
# - 打包不会改动 git 工作区。
# - 两个 exe 是确定性构建（固定 flags，可复现）；zip 记录文件 mtime、NSIS 内嵌构建时间，
#   因此 zip/安装包的 sha256 每次构建都不同，发布记录以当次输出为准。

SHELL := /bin/bash
.DEFAULT_GOAL := help

ROOT := $(CURDIR)
WEB := $(ROOT)/web
EDGE := $(ROOT)/edge
DESKTOP := $(ROOT)/desktop/MeerkatDesktop
BUILD_WIN := $(DESKTOP)/build/windows
WIN_DIR := $(DESKTOP)/bin/Meerkat-Windows
VERSION ?= $(shell tr -d '[:space:]' < "$(ROOT)/VERSION")
UPDATE_PUBLIC_KEY ?= $(shell tr -d '[:space:]' < "$(ROOT)/scripts/release-public-key.txt")
APP_LDFLAGS := -X main.appVersion=$(VERSION) -X main.updatePublicKey=$(UPDATE_PUBLIC_KEY)
WIN_EXE := $(WIN_DIR)/Meerkat.exe
WIN_EDGE := $(WIN_DIR)/meerkat-edge.exe
WIN_ZIP := $(DESKTOP)/bin/Meerkat-Windows-v$(VERSION).zip
WIN_SETUP := $(DESKTOP)/bin/Meerkat-Setup-$(VERSION).exe
MAC_APP := $(DESKTOP)/bin/Meerkat.app

NSIS_IMAGE ?= meerkat-nsis:3.08
DOCKER ?= $(shell command -v docker 2>/dev/null || { [ -x "$(HOME)/.orbstack/bin/docker" ] && printf '%s' "$(HOME)/.orbstack/bin/docker"; })

# Go 环境：优先仓库内缓存；GOTOOLCHAIN=auto 让旧 Go 使用 go.mod 指定的工具链。
GOENV := GOTOOLCHAIN=auto $(if $(wildcard $(ROOT)/.runtime/gocache),GOCACHE=$(ROOT)/.runtime/gocache) $(if $(wildcard $(ROOT)/.runtime/mod),GOMODCACHE=$(ROOT)/.runtime/mod)

START_TIME := $(shell date +%s)
WIN_ARTIFACTS := $(WIN_EXE) $(WIN_EDGE) $(WIN_ZIP) $(WIN_SETUP)
PORTABLE_ARTIFACTS := $(WIN_EXE) $(WIN_EDGE) $(WIN_ZIP)

# 打印产物路径、大小与 sha256（目录按 du 统计，不计算单一 sha256）。
define ARTIFACT_REPORT
for f in $(1); do if [ ! -e "$$f" ]; then printf '  缺失：%s\n' "$$f"; continue; fi; if [ -d "$$f" ]; then size=$$(du -sk "$$f" | awk '{print $$1 * 1024}'); hash='目录产物（无单一 sha256）'; else size=$$(wc -c < "$$f" | tr -d ' '); hash=$$(if command -v sha256sum >/dev/null 2>&1; then sha256sum "$$f" | cut -d' ' -f1; else shasum -a 256 "$$f" | cut -d' ' -f1; fi); fi; awk -v f="$$f" -v s="$$size" -v h="$$hash" 'BEGIN { printf "  %s\n    %s B (%.2f MiB)\n    sha256 %s\n", f, s, s / 1048576, h }'; done
endef

.PHONY: help web desktop-win edge-win edge-darwin zip-portable nsis-installer package-win-portable package-win package-macos package-all release clean

help: ## 显示可用目标
	@set -euo pipefail; printf 'Meerkat 打包目标：\n'; awk 'BEGIN { FS = ":.*## " } /^[a-zA-Z0-9_-]+:.*## / { printf "  %-22s %s\n", $$1, $$2 }' $(lastword $(MAKEFILE_LIST))

web: ## 构建前端单文件（vite）并复制到 Wails 嵌入目录
	@set -euo pipefail; [ -d $(WEB)/node_modules ] || { printf '缺少 web/node_modules，请先执行：cd web && pnpm install\n'; exit 1; }; cd $(WEB) && npx vite build && mkdir -p $(DESKTOP)/frontend/dist && cp dist/index.html $(DESKTOP)/frontend/dist/index.html && printf '前端单文件：%s（%s B）\n' "$(DESKTOP)/frontend/dist/index.html" "$$(wc -c < $(DESKTOP)/frontend/dist/index.html | tr -d ' ')"

desktop-win: web ## 编译 Windows 桌面端 Meerkat.exe（GUI 子系统，避免控制台窗口）
	@set -euo pipefail; mkdir -p $(WIN_DIR); cd $(DESKTOP) && env $(GOENV) GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui $(APP_LDFLAGS)" -o bin/Meerkat-Windows/Meerkat.exe . && printf 'Meerkat.exe：%s B\n' "$$(wc -c < $(WIN_EXE) | tr -d ' ')"

edge-win: ## 编译 Windows Edge sidecar（console 子系统，便于看日志）并放入免安装目录
	@set -euo pipefail; cd $(EDGE) && env $(GOENV) GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o bin/meerkat-edge-windows-amd64.exe ./cmd/meerkat-edge && mkdir -p $(WIN_DIR) && cp bin/meerkat-edge-windows-amd64.exe $(WIN_DIR)/meerkat-edge.exe && printf 'meerkat-edge.exe：%s B\n' "$$(wc -c < $(WIN_EDGE) | tr -d ' ')"

edge-darwin: ## 编译 macOS Edge sidecar（bundle.sh 会把它放进 Meerkat.app）
	@set -euo pipefail; cd $(EDGE) && env $(GOENV) GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o bin/meerkat-edge-darwin-arm64 ./cmd/meerkat-edge && printf 'meerkat-edge-darwin-arm64：%s B\n' "$$(wc -c < $(EDGE)/bin/meerkat-edge-darwin-arm64 | tr -d ' ')"

zip-portable: desktop-win edge-win ## 打免安装目录 zip（Meerkat-Windows-vX.Y.Z.zip）
	@set -euo pipefail; cd $(DESKTOP)/bin && rm -f Meerkat-Windows-v$(VERSION).zip && zip -r -X -q Meerkat-Windows-v$(VERSION).zip Meerkat-Windows && unzip -t -q Meerkat-Windows-v$(VERSION).zip >/dev/null && printf '免安装 zip：%s\n' "$(WIN_ZIP)"

nsis-installer: desktop-win edge-win ## 生成 NSIS 安装包：优先容器，其次本机 makensis
	@set -euo pipefail; if [ -n "$(DOCKER)" ]; then printf '使用容器镜像 %s 构建安装包…\n' "$(NSIS_IMAGE)"; "$(DOCKER)" image inspect $(NSIS_IMAGE) >/dev/null 2>&1 || { printf '缺少镜像 %s，请先构建或拉取该镜像\n' "$(NSIS_IMAGE)"; exit 1; }; "$(DOCKER)" run --rm -u "$$(id -u):$$(id -g)" -e HOME=/tmp -v "$(DESKTOP)":/work -w /work/build/windows $(NSIS_IMAGE) makensis -DAPP_VERSION="$(VERSION)" installer.nsi; elif command -v makensis >/dev/null 2>&1; then printf '使用本机 makensis…\n'; cd $(BUILD_WIN) && makensis -DAPP_VERSION="$(VERSION)" installer.nsi; else printf '未找到 docker 或 makensis：无法生成 Windows 安装包（可先安装 Docker 或 NSIS）\n'; exit 1; fi; [ -f $(WIN_SETUP) ] || { printf '安装包未生成：%s\n' "$(WIN_SETUP)"; exit 1; }; printf '安装包：%s\n' "$(WIN_SETUP)"

package-win-portable: zip-portable ## 免安装包：前端 + 两个 exe + zip（不需要 docker）
	@set -euo pipefail; printf '\n免安装包产物：\n'; $(call ARTIFACT_REPORT,$(PORTABLE_ARTIFACTS)); printf '\n耗时：%ss\n' "$$(( $$(date +%s) - $(START_TIME) ))"

package-win: package-win-portable nsis-installer ## Windows 全量：exe + zip + NSIS 安装包
	@set -euo pipefail; printf '\nWindows 产物：\n'; $(call ARTIFACT_REPORT,$(WIN_ARTIFACTS)); printf '\n可执行文件类型：\n'; file $(WIN_EXE) $(WIN_EDGE); printf '\n总耗时：%ss\n' "$$(( $$(date +%s) - $(START_TIME) ))"

package-macos: web edge-darwin ## macOS Meerkat.app（bundle.sh 组装 + ad-hoc 签名）
	@set -euo pipefail; cd $(DESKTOP) && env $(GOENV) VERSION="$(VERSION)" UPDATE_PUBLIC_KEY="$(UPDATE_PUBLIC_KEY)" bash bundle.sh && printf '\nmacOS 产物：\n' && $(call ARTIFACT_REPORT,$(MAC_APP)) && printf '  主程序 sha256：%s\n' "$$(if command -v sha256sum >/dev/null 2>&1; then sha256sum $(MAC_APP)/Contents/MacOS/meerkatdesktop; else shasum -a 256 $(MAC_APP)/Contents/MacOS/meerkatdesktop; fi | cut -d' ' -f1)" && printf '\n总耗时：%ss\n' "$$(( $$(date +%s) - $(START_TIME) ))"

package-all: package-win package-macos ## Windows + macOS 全量打包
	@set -euo pipefail; printf '\n全部产物：\n'; $(call ARTIFACT_REPORT,$(WIN_ARTIFACTS) $(MAC_APP)); printf '\n总耗时：%ss\n' "$$(( $$(date +%s) - $(START_TIME) ))"

release: ## 构建并上传 Windows 安装包与签名更新清单
	@set -euo pipefail; node scripts/release-update.mjs verify-release "$(VERSION)"; key="$$(node scripts/release-update.mjs public-key)"; $(MAKE) nsis-installer VERSION="$(VERSION)" UPDATE_PUBLIC_KEY="$$key"; node scripts/release-update.mjs publish "$(VERSION)" "$(WIN_SETUP)"

clean: ## 删除构建产物（保留 .runtime 缓存与用户配置）
	@set -euo pipefail; rm -rf $(WEB)/dist $(DESKTOP)/frontend/dist $(DESKTOP)/bin $(EDGE)/bin; printf '已清理：web/dist、desktop/MeerkatDesktop/frontend/dist、desktop/MeerkatDesktop/bin、edge/bin\n'
