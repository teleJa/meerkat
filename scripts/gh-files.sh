#!/usr/bin/env bash
# 免 GitHub API 的仓库文件浏览 / 取文件内容：只用 git over SSH 与 raw.githubusercontent.com。
#
# 为什么需要它：研究阶段要在一个仓库里翻文件（看是不是工具、有没有 CLI、README 说什么），
# 走 api.github.com 会撞 60 次/小时（按出口 IP，代理节点共享）的限额；而 git over SSH
# 与 raw.githubusercontent.com 都不属于 API 调用。
#
# 用法：
#   scripts/gh-files.sh list  owner/repo                 # 列默认分支全部文件
#   scripts/gh-files.sh list  owner/repo 'cmd/**'        # 只列匹配的文件（git ls-tree + shell glob）
#   scripts/gh-files.sh read  owner/repo path/to/file    # 取文件内容到 stdout
#   scripts/gh-files.sh tree  owner/repo                 # 只看顶层目录/文件
#
#   BRANCH=develop scripts/gh-files.sh list owner/repo   # 指定分支（默认用远端 HEAD）
#   CACHE=/tmp/ghfiles scripts/gh-files.sh list owner/repo  # 复用浅克隆缓存（默认 $TMPDIR 下）
#
# 原理：`git clone --depth 1 --filter=blob:none --no-checkout`（只取提交与树，不取文件内容），
# 之后 `git ls-tree` 列表、`git show` 取内容都是本地操作。首次克隆有网络往返，
# 同一仓库后续查询走缓存，几百次浏览也只克隆一次。
#
# 退出码：0 成功；1 失败；2 用法错误。
set -uo pipefail

CACHE_ROOT="${CACHE:-${TMPDIR:-/tmp}/gh-files-cache}"
BRANCH="${BRANCH:-}"
cmd="${1:-}"
shift 2>/dev/null || true

usage() {
  sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

[ -n "$cmd" ] || usage
repo="${1:-}"
action_path="${2:-}"
[ -n "$repo" ] || usage

# 归一化 owner/repo，接受完整 URL 与 git@ 形式。
repo="${repo#https://github.com/}"
repo="${repo#git@github.com:}"
repo="${repo%.git}"
if ! [[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "仓库名不是 owner/repo 形式: $repo" >&2
  exit 2
fi

slug="$(echo "$repo" | tr '/' '_')"
cache_dir="$CACHE_ROOT/$slug"

# ensure_clone —— 首次调用时做一次浅克隆；已存在则直接复用。
ensure_clone() {
  if [ -d "$cache_dir/.git" ]; then
    return 0
  fi
  mkdir -p "$CACHE_ROOT" 2>/dev/null || {
    echo "无法创建缓存目录 $CACHE_ROOT" >&2
    return 1
  }
  local args=(clone --depth 1 --filter=blob:none --no-checkout --quiet)
  if [ -n "$BRANCH" ]; then
    args+=(--branch "$BRANCH")
  fi
  args+=("git@github.com:$repo.git" "$cache_dir")
  # 克隆走 SSH：已注册的公钥即可，不需要 token，也不吃 API 限额。
  GIT_TERMINAL_PROMPT=0 git "${args[@]}" 2>&1 | sed 's/^/  /'
  [ -d "$cache_dir/.git" ] || return 1
}

case "$cmd" in
  list)
    ensure_clone || { echo "克隆 $repo 失败（仓库不存在、无权限或 SSH 未就绪）" >&2; exit 1; }
    if [ -n "$action_path" ]; then
      ( cd "$cache_dir" && git ls-tree -r --name-only HEAD | grep -E "$action_path" )
    else
      ( cd "$cache_dir" && git ls-tree -r --name-only HEAD )
    fi
    ;;
  tree)
    ensure_clone || exit 1
    ( cd "$cache_dir" && git ls-tree --name-only HEAD )
    ;;
  read)
    [ -n "$action_path" ] || { echo "read 需要文件路径" >&2; exit 2; }
    ensure_clone || exit 1
    ( cd "$cache_dir" && GIT_TERMINAL_PROMPT=0 git show "HEAD:$action_path" ) || {
      echo "读取 $repo:$action_path 失败（路径不存在？）" >&2
      exit 1
    }
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    echo "未知子命令: $cmd" >&2
    usage
    ;;
esac
