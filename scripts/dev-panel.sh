#!/usr/bin/env bash
# 构建 Web 面板并启动托管它的中心（Ctrl-C 退出）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CENTER_PORT="${MEERKAT_PORT:-8787}"

echo "[dev-panel] 构建 Web 面板…"
( cd "${ROOT}/web" && pnpm build >/dev/null )

echo "[dev-panel] 启动中心：http://127.0.0.1:${CENTER_PORT}/#/live"
echo "[dev-panel] 登录令牌：${ROOT}/center/data/admin-token（首次启动生成）"
cd "${ROOT}/center"
MEERKAT_HOST=0.0.0.0 MEERKAT_PORT="${CENTER_PORT}" MEERKAT_WEB_DIST="${ROOT}/web/dist" pnpm dev
