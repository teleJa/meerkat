#!/usr/bin/env bash
# P1 本机联调：center + 本机 edge，验证注册、WS 连接、心跳与指标上报、重启 center 后自动重连。
set -euo pipefail
export NODE_OPTIONS="${NODE_OPTIONS:-} --no-warnings"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${ROOT}/.runtime/local-integration"
CENTER_DATA="${RUN_DIR}/center"
EDGE_STATE="${RUN_DIR}/edge"
EDGE_BIN="${ROOT}/edge/bin/meerkat-edge"
CENTER_PORT="${MEERKAT_TEST_PORT:-8787}"
CENTER_URL="http://127.0.0.1:${CENTER_PORT}"
EDGE_NAME="${MEERKAT_TEST_EDGE_NAME:-Meerkat 本机 Edge}"
EDGE_ROLE="${MEERKAT_TEST_EDGE_ROLE:-Edge · 本机}"
EDGE_HOSTNAME="$(hostname)"
CENTER_PID=""
EDGE_PID=""

log() { printf '[%s] %s\n' "$(date '+%H:%M:%S')" "$*"; }

port_pids() {
  lsof -nP -tiTCP:"${CENTER_PORT}" -sTCP:LISTEN 2>/dev/null || true
}

stop_center() {
  if [[ -n "${CENTER_PID}" ]] && kill -0 "${CENTER_PID}" 2>/dev/null; then
    kill "${CENTER_PID}" 2>/dev/null || true
    wait "${CENTER_PID}" 2>/dev/null || true
  fi
  CENTER_PID=""
  local pids
  pids=$(port_pids)
  if [[ -n "$pids" ]]; then
    for pid in $pids; do kill "${pid}" 2>/dev/null || true; done
  fi
  for _ in $(seq 1 40); do
    [[ -z "$(port_pids)" ]] && return 0
    sleep 0.25
  done
  log "端口 ${CENTER_PORT} 仍被占用，强制结束"
  for pid in $(port_pids); do kill -9 "${pid}" 2>/dev/null || true; done
}

cleanup() {
  if [[ -n "${EDGE_PID}" ]] && kill -0 "${EDGE_PID}" 2>/dev/null; then
    kill "${EDGE_PID}" 2>/dev/null || true
    wait "${EDGE_PID}" 2>/dev/null || true
  fi
  EDGE_PID=""
  stop_center
}
trap cleanup EXIT

json_field() {
  node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{const j=JSON.parse(d);console.log(j?.[process.argv[1]]??"")})' "$1"
}

start_center() {
  if [[ -n "$(port_pids)" ]]; then
    log "端口 ${CENTER_PORT} 已被占用，联调终止（pid: $(port_pids | tr '\n' ' ')）"
    exit 1
  fi
  ( cd "${ROOT}/center" && MEERKAT_HOST=0.0.0.0 MEERKAT_PORT="${CENTER_PORT}" MEERKAT_DATA_DIR="${CENTER_DATA}" \
      pnpm exec tsx src/server.ts >> "${RUN_DIR}/center.log" 2>&1 ) &
  CENTER_PID=$!
  for _ in $(seq 1 120); do
    if curl -sf "${CENTER_URL}/api/health" >/dev/null 2>&1; then
      log "center 已就绪: ${CENTER_URL}（启动 pid ${CENTER_PID}，监听 pid $(port_pids | tr '\n' ' ')）"
      return 0
    fi
    sleep 0.25
  done
  log "center 启动失败，日志尾部："
  tail -n 40 "${RUN_DIR}/center.log" || true
  exit 1
}

device_json() {
  curl -s "${CENTER_URL}/api/devices" -H "Authorization: Bearer ${ADMIN_TOKEN}" | node -e '
    let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{
      const j=JSON.parse(d);
      const dev=j.devices.find(x=>x.hostname===process.argv[1]);
      if (dev) process.stdout.write(JSON.stringify(dev));
    })' "${EDGE_HOSTNAME}"
}

# 等待设备 online *且* 已有指标；可选要求 last_seen 与 before 不同（重连证明）。
wait_online() {
  local label="$1" before_last_seen="$2"
  for _ in $(seq 1 60); do
    local device status last_seen metrics
    device=$(device_json)
    if [[ -n "$device" ]]; then
      status=$(printf '%s' "$device" | json_field status)
      last_seen=$(printf '%s' "$device" | json_field last_seen_at)
      metrics=$(printf '%s' "$device" | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{const j=JSON.parse(d);console.log(j.metrics?.["cpu_pct"]??"")})')
      if [[ "$status" == "online" && -n "$metrics" && ( -z "$before_last_seen" || "$last_seen" != "$before_last_seen" ) ]]; then
        log "$label: device online 且已有指标（last_seen=${last_seen}）"
        printf '%s' "$device" > "${RUN_DIR}/${label}.json"
        return 0
      fi
    fi
    sleep 0.5
  done
  log "$label: 等待 online + 指标超时"
  curl -s "${CENTER_URL}/api/devices" -H "Authorization: Bearer ${ADMIN_TOKEN}" || true
  exit 1
}

connected_events() {
  curl -s "${CENTER_URL}/api/events?limit=200" -H "Authorization: Bearer ${ADMIN_TOKEN}" | node -e '
    let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{
      const j=JSON.parse(d);
      console.log(j.events.filter(e=>e.type==="device.connected").length);
    })'
}

rm -rf "${RUN_DIR}"
mkdir -p "${RUN_DIR}" "${CENTER_DATA}" "${EDGE_STATE}"

if [[ ! -x "${EDGE_BIN}" ]]; then
  for candidate in "${ROOT}/edge/bin/meerkat-edge-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m)" "${ROOT}/edge/bin/meerkat-edge"; do
    if [[ -x "${candidate}" ]]; then EDGE_BIN="${candidate}"; break; fi
  done
fi
if [[ ! -x "${EDGE_BIN}" ]]; then
  log "未找到 edge 二进制，尝试构建"
  ( cd "${ROOT}/edge" && mkdir -p bin && go build -o bin/meerkat-edge ./cmd/meerkat-edge )
  EDGE_BIN="${ROOT}/edge/bin/meerkat-edge"
fi
log "使用 edge 二进制: ${EDGE_BIN}"

start_center
ADMIN_TOKEN="$(cat "${CENTER_DATA}/admin-token")"

log "1/5 创建配对令牌"
PAIR_RESPONSE=$(curl -sf -X POST "${CENTER_URL}/api/pairing-tokens" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" -H 'content-type: application/json' \
  -d '{"label":"local-edge-integration","ttl_seconds":600}')
PAIR_TOKEN=$(printf '%s' "$PAIR_RESPONSE" | json_field token)
[[ -n "$PAIR_TOKEN" ]] || { log "配对令牌创建失败"; exit 1; }
log "配对令牌已创建（不回显）"

log "2/5 edge pair"
"${EDGE_BIN}" pair --center "${CENTER_URL}" --token "${PAIR_TOKEN}" \
  --state-dir "${EDGE_STATE}" --name "${EDGE_NAME}" --role "${EDGE_ROLE}" \
  >> "${RUN_DIR}/edge.log" 2>&1

log "3/5 edge run"
"${EDGE_BIN}" run --state-dir "${EDGE_STATE}" >> "${RUN_DIR}/edge.log" 2>&1 &
EDGE_PID=$!

wait_online "device-online" ""
DEVICE_JSON=$(cat "${RUN_DIR}/device-online.json")
DEVICE_ID=$(printf '%s' "$DEVICE_JSON" | json_field id)
EVENTS_BEFORE=$(connected_events)
log "首次连接完成: ${DEVICE_ID}（connected 事件累计 ${EVENTS_BEFORE}）"

log "4/5 重启 center，验证 edge 自动重连"
LAST_SEEN_BEFORE=$(printf '%s' "$DEVICE_JSON" | json_field last_seen_at)
stop_center
log "center 已停止，端口是否释放: $( [[ -z "$(port_pids)" ]] && echo yes || echo no )"
sleep 2
start_center
wait_online "device-reconnected" "${LAST_SEEN_BEFORE}"
EVENTS_AFTER=$(connected_events)
log "重连后 connected 事件累计 ${EVENTS_AFTER}（之前 ${EVENTS_BEFORE}）"
if (( EVENTS_AFTER <= EVENTS_BEFORE )); then
  log "重连未产生新的 device.connected 事件，判定失败"
  exit 1
fi

log "5/5 读取指标历史与事件"
curl -s "${CENTER_URL}/api/devices/${DEVICE_ID}/metrics?limit=5" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" > "${RUN_DIR}/metrics.json"
curl -s "${CENTER_URL}/api/events?limit=50" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" > "${RUN_DIR}/events.json"

log "验证通过。产物目录: ${RUN_DIR}"
log "  device-online.json / device-reconnected.json / metrics.json / events.json / center.log / edge.log"

if [[ "${MEERKAT_TEST_STALE:-0}" == "1" ]]; then
  log "附加：停止 edge，等待 stale 判定（约 35s）"
  kill "${EDGE_PID}" 2>/dev/null || true
  wait "${EDGE_PID}" 2>/dev/null || true
  EDGE_PID=""
  STATUS=""
  for _ in $(seq 1 45); do
    STATUS=$(device_json | json_field status)
    [[ "$STATUS" == "stale" ]] && break
    sleep 1
  done
  [[ "${STATUS:-}" == "stale" ]] && log "stale 判定生效" || { log "未观察到 stale，当前: ${STATUS:-unknown}"; exit 1; }
fi
