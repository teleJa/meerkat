#!/usr/bin/env bash
# 免 GitHub API 的仓库元数据查询：不依赖 gh CLI、不需要 token、不受 API 限流。
#
# 为什么需要它：未认证的 api.github.com 限额是 60 次/小时，而且是**按出口 IP** 算的；
# 走代理时整条节点共享这 60 次，批量筛仓库（几百条）几秒就打满 403。
# 网页 HTML 与 raw.githubusercontent.com 不属于 API 调用，没有这个限制。
#
# 取到的字段：
#   stars / forks       —— 仓库首页 HTML 里的 "stargazerCount" / "forksCount"
#   license             —— HTML 里 license 卡片的 "tabName"（MIT / Apache-2.0 等）
#   created_at          —— HTML 里的 "createdAt"
#   pushed_at           —— commits atom feed（/commits/<branch>.atom）最新一条 <updated>
#   archived            —— HTML 里的 "isArchived" 或页面上的 Archived 标记
#
# 用法：
#   scripts/gh-meta.sh owner/repo [owner/repo ...]
#   scripts/gh-meta.sh --json owner/repo          # JSON 输出，便于流水线
#   BRANCH=develop scripts/gh-meta.sh owner/repo  # 指定分支（默认 main，取不到会试 master）
#   PROXY=http://127.0.0.1:7899 scripts/gh-meta.sh owner/repo
#
# 退出码：0 至少有一个仓库成功；1 全部失败；2 用法错误。
set -uo pipefail

PROXY_DEFAULT="${PROXY:-http://127.0.0.1:7899}"
PROXY="$PROXY_DEFAULT"
BRANCH="${BRANCH:-main}"
JSON=0
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

usage() {
  sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

repos=()
while [ $# -gt 0 ]; do
  case "$1" in
    --json) JSON=1; shift ;;
    --no-proxy) PROXY=""; shift ;;
    -h|--help) usage ;;
    --*) echo "未知参数: $1" >&2; exit 2 ;;
    *) repos+=("$1"); shift ;;
  esac
done

if [ ${#repos[@]} -eq 0 ]; then
  usage
fi

# fetch URL OUTFILE —— 有代理走代理，没代理直连；不打印进度噪声。
fetch() {
  local url="$1" out="$2"
  local args=(-sL -m 30 --retry 1 --retry-delay 1 -o "$out" -w '%{http_code}')
  if [ -n "$PROXY" ]; then
    args=(-x "$PROXY" "${args[@]}")
  fi
  curl "${args[@]}" "$url"
}

# field FILE PATTERN —— 取第一个匹配模式的 JSON 值（去引号）。取不到输出空。
field() {
  local file="$1" pattern="$2"
  [ -f "$file" ] || { echo ""; return; }
  grep -oE "$pattern" "$file" 2>/dev/null | head -1 | sed -E 's/.*:"([^"]*)".*/\1/'
}

# numeric FILE NAME —— 取 "name":12345 的数字。
numeric() {
  local file="$1" name="$2"
  [ -f "$file" ] || { echo ""; return; }
  grep -oE "\"$name\":[0-9]+" "$file" 2>/dev/null | head -1 | sed -E "s/.*:([0-9]+).*/\1/"
}

# license FILE —— 优先 `"spdxId":"MIT"`（GitHub 归一化后的 SPDX 标识，最可靠）。
# 退化情形：
#   - NOASSERTION：GitHub 无法整仓判定（例如 torvalds/linux 这种多许可证大仓），
#     如实输出 NOASSERTION，不要瞎猜；
#   - 完全没有 spdxId：退回 license 卡片的 tabName。
license_of() {
  local file="$1" value
  [ -f "$file" ] || { echo ""; return; }
  value="$(grep -oE '"spdxId":"[^"]*"' "$file" 2>/dev/null | head -1 | sed -E 's/.*:"([^"]*)".*/\1/')"
  if [ -n "$value" ]; then
    echo "$value"
    return
  fi
  value="$(grep -oE '"tabName":"[A-Za-z0-9.+-]+"' "$file" 2>/dev/null |
    sed -E 's/.*:"([^"]*)".*/\1/' |
    grep -viE '^(README|LICENSE|License|CODE_OF_CONDUCT|CONTRIBUTING|Contributing|Code of conduct|Security|SECURITY)$' | head -1)"
  echo "$value"
}

# archived FILE —— HTML 里有 isArchived:true 或 Archived 徽标即视为归档。
archived_of() {
  local file="$1"
  [ -f "$file" ] || { echo "unknown"; return; }
  if grep -qE '"isArchived":true|This repository has been archived' "$file" 2>/dev/null; then
    echo "true"
  elif grep -qE '"isArchived":false' "$file" 2>/dev/null; then
    echo "false"
  else
    echo "unknown"
  fi
}

# last_commit REPO —— commits atom feed 最新一条的 <updated>，不带 API。
last_commit() {
  local repo="$1" atom="$WORKDIR/atom.xml" code branch
  for branch in "$BRANCH" master; do
    code="$(fetch "https://github.com/$repo/commits/$branch.atom" "$atom")"
    if [ "$code" = "200" ] && grep -q '<entry>' "$atom" 2>/dev/null; then
      # feed 级 <updated> 与第一条 entry 的 <updated> 常常相同；取 entry 内的那条更准。
      awk '/<entry>/{found=1} found && /<updated>/{gsub(/.*<updated>|<\/updated>.*/,""); print; exit}' "$atom"
      return 0
    fi
  done
  echo ""
  return 1
}

ok=0
total=0
results=()

for repo in "${repos[@]}"; do
  total=$((total + 1))
  repo="${repo#https://github.com/}"
  repo="${repo#git@github.com:}"
  repo="${repo%.git}"
  if ! [[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
    results+=("$(printf '%-52s ERROR 仓库名不是 owner/repo 形式' "$repo")")
    continue
  fi

  page="$WORKDIR/page.html"
  code="$(fetch "https://github.com/$repo" "$page")"
  if [ "$code" != "200" ]; then
    results+=("$(printf '%-52s ERROR HTTP %s（仓库不存在或无权限）' "$repo" "$code")")
    continue
  fi

  stars="$(numeric "$page" stargazerCount)"
  forks="$(numeric "$page" forksCount)"
  created="$(field "$page" '"createdAt":"[^"]*"')"
  lic="$(license_of "$page")"
  arch="$(archived_of "$page")"
  pushed="$(last_commit "$repo")"
  [ -n "$pushed" ] || pushed="$created"

  ok=$((ok + 1))

  if [ "$JSON" = "1" ]; then
    results+=("$(printf '{"repo":"%s","stars":%s,"forks":%s,"license":"%s","archived":"%s","created_at":"%s","pushed_at":"%s"}' \
      "$repo" "${stars:-null}" "${forks:-null}" "${lic:-}" "$arch" "${created:-}" "${pushed:-}")")
  else
    results+=("$(printf '%-52s %6s stars  %5s forks  %-14s %-8s pushed %s' \
      "$repo" "${stars:-?}" "${forks:-?}" "${lic:-?}" "archived=$arch" "${pushed:-?}")")
  fi
done

if [ "$JSON" = "1" ]; then
  printf '[\n'
  for i in "${!results[@]}"; do
    if [ "$i" -lt $((${#results[@]} - 1)) ]; then
      printf '  %s,\n' "${results[$i]}"
    else
      printf '  %s\n' "${results[$i]}"
    fi
  done
  printf ']\n'
else
  printf '%s\n' "${results[@]}"
fi

[ "$ok" -gt 0 ] && exit 0 || exit 1
