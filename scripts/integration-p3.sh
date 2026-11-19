#!/usr/bin/env bash
# P3 端到端联调：center + 本机 edge + 假 Claude CLI（真实验证指令/事件/outbox/取消）。
set -euo pipefail
export NODE_OPTIONS="${NODE_OPTIONS:-} --no-warnings"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${ROOT}/.runtime/p3"
CENTER_DATA="${RUN_DIR}/center"
EDGE_STATE="${RUN_DIR}/edge"
EDGE_BIN="${ROOT}/edge/bin/meerkat-edge-darwin-arm64"
CENTER_PORT="${MEERKAT_TEST_PORT:-8787}"
CENTER_URL="http://127.0.0.1:${CENTER_PORT}"
CENTER_PID=""
EDGE_PID=""

log() { printf '[%s] %s\n' "$(date '+%H:%M:%S')" "$*"; }
json() { node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{const j=JSON.parse(d);'"$1"'})'; }

port_pids() { lsof -nP -tiTCP:"${CENTER_PORT}" -sTCP:LISTEN 2>/dev/null || true; }
stop_center() {
  if [[ -n "${CENTER_PID}" ]] && kill -0 "${CENTER_PID}" 2>/dev/null; then
    kill "${CENTER_PID}" 2>/dev/null || true; wait "${CENTER_PID}" 2>/dev/null || true
  fi
  CENTER_PID=""
  for pid in $(port_pids); do kill "${pid}" 2>/dev/null || true; done
  for _ in $(seq 1 40); do [[ -z "$(port_pids)" ]] && return 0; sleep 0.25; done
}
cleanup() {
  if [[ -n "${EDGE_PID}" ]] && kill -0 "${EDGE_PID}" 2>/dev/null; then
    kill "${EDGE_PID}" 2>/dev/null || true; wait "${EDGE_PID}" 2>/dev/null || true
  fi
  EDGE_PID=""
  stop_center
}
trap cleanup EXIT

start_center() {
  ( cd "${ROOT}/center" && MEERKAT_HOST=0.0.0.0 MEERKAT_PORT="${CENTER_PORT}" MEERKAT_DATA_DIR="${CENTER_DATA}" \
      pnpm exec tsx src/server.ts >> "${RUN_DIR}/center.log" 2>&1 ) &
  CENTER_PID=$!
  for _ in $(seq 1 120); do
    curl -sf "${CENTER_URL}/api/health" >/dev/null 2>&1 && { log "center 已就绪"; return 0; }
    sleep 0.25
  done
  log "center 启动失败"; tail -30 "${RUN_DIR}/center.log"; exit 1
}

api() {
  local method="$1" path="$2" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -s -X "$method" "${CENTER_URL}${path}" -H "Authorization: Bearer ${ADMIN}" -H 'content-type: application/json' -d "$body"
  else
    curl -s -X "$method" "${CENTER_URL}${path}" -H "Authorization: Bearer ${ADMIN}"
  fi
}

wait_task() {
  local task_id="$1" want="$2" timeout="${3:-60}"
  for _ in $(seq 1 "$((timeout * 2))"); do
    local task
    task=$(api GET "/api/tasks/${task_id}" | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{const j=JSON.parse(d);console.log(j.task.status)})')
    if [[ "$task" == "$want" ]]; then log "task ${task_id} -> ${want}"; return 0; fi
    if [[ "$task" == "failed" || "$task" == "cancelled" ]] && [[ "$want" != "$task" ]]; then
      log "task ${task_id} 提前结束: ${task}"; api GET "/api/tasks/${task_id}" | head -c 1500; echo; return 1
    fi
    sleep 0.5
  done
  log "task ${task_id} 等待 ${want} 超时"; return 1
}

rm -rf "${RUN_DIR}"
mkdir -p "${RUN_DIR}" "${CENTER_DATA}" "${EDGE_STATE}"
[[ -x "${EDGE_BIN}" ]] || { log "缺少 ${EDGE_BIN}"; exit 1; }
[[ -x "${ROOT}/scripts/fake-claude.mjs" ]] || { log "缺少 fake-claude"; exit 1; }

start_center
ADMIN="$(cat "${CENTER_DATA}/admin-token")"

log "1/7 配对 Edge（harness=fake-claude）"
PAIR_TOKEN=$(api POST /api/pairing-tokens '{"label":"p3-e2e","ttl_seconds":600}' | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{console.log(JSON.parse(d).token)})')
"${EDGE_BIN}" pair --center "${CENTER_URL}" --token "$PAIR_TOKEN" --state-dir "${EDGE_STATE}" \
  --name "P3 E2E Edge" --role "Edge · 测试" >> "${RUN_DIR}/edge.log" 2>&1

log "2/7 启动 Edge"
"${EDGE_BIN}" run --state-dir "${EDGE_STATE}" --allow-root "${ROOT}" \
  --harness-cmd "${ROOT}/scripts/fake-claude.mjs" >> "${RUN_DIR}/edge.log" 2>&1 &
EDGE_PID=$!

for _ in $(seq 1 60); do
  DEVICE=$(api GET /api/devices | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{const j=JSON.parse(d);const x=j.devices.find(v=>v.hostname===process.argv[1]);console.log(x?x.id:"")})' "$(hostname)")
  [[ -n "$DEVICE" ]] && break
  sleep 0.5
done
[[ -n "${DEVICE:-}" ]] || { log "设备未注册"; exit 1; }
log "设备 ${DEVICE} 已就绪"

log "3/7 派发任务并等待完成"
TASK=$(api POST /api/tasks "{\"request\":\"你好，回复 OK\",\"device_id\":\"${DEVICE}\",\"cwd\":\"${ROOT}\"}")
TASK_ID=$(printf '%s' "$TASK" | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{console.log(JSON.parse(d).task.id)})')
log "task=${TASK_ID}"
wait_task "$TASK_ID" succeeded 60
DETAIL=$(api GET "/api/tasks/${TASK_ID}")
printf '%s' "$DETAIL" > "${RUN_DIR}/task-detail.json"
node -e 'const j=require("'"${RUN_DIR}/task-detail.json"'");console.log(JSON.stringify({status:j.task.status,evidence:j.task.evidence_kind,turns:j.turns.length,events:j.events.length,result:j.turns[0]?.result,harness:j.session?.harness_session_id},null,2))'

log "4/7 追问（send_turn + --resume）"
FOLLOW=$(api POST "/api/tasks/${TASK_ID}/turns" '{"request":"再补充一句"}')
FOLLOW_TURN=$(printf '%s' "$FOLLOW" | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{console.log(JSON.parse(d).turn_id)})')
wait_task "$TASK_ID" succeeded 60
api GET "/api/tasks/${TASK_ID}" > "${RUN_DIR}/task-followup.json"
node -e 'const j=require("'"${RUN_DIR}/task-followup.json"'");console.log(JSON.stringify({turns:j.turns.length,followup:j.turns[1]?.request,status:j.task.status}))'

log "5/7 取消（SLOW 任务）"
SLOW=$(api POST /api/tasks "{\"request\":\"SLOW 请等待\",\"device_id\":\"${DEVICE}\",\"cwd\":\"${ROOT}\"}")
SLOW_ID=$(printf '%s' "$SLOW" | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{console.log(JSON.parse(d).task.id)})')
wait_task "$SLOW_ID" running 20
api POST "/api/tasks/${SLOW_ID}/cancel" > "${RUN_DIR}/cancel.json"
wait_task "$SLOW_ID" cancelled 30

log "6/7 outbox：任务执行中重启 center"
BUF=$(api POST /api/tasks "{\"request\":\"DELAY6000 缓冲测试\",\"device_id\":\"${DEVICE}\",\"cwd\":\"${ROOT}\"}")
BUF_ID=$(printf '%s' "$BUF" | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{console.log(JSON.parse(d).task.id)})')
wait_task "$BUF_ID" running 20
stop_center
log "center 已停止，等待 Edge 产生未确认事件"
sleep 3
start_center
wait_task "$BUF_ID" succeeded 60
api GET "/api/tasks/${BUF_ID}" > "${RUN_DIR}/task-buffered.json"
node -e 'const j=require("'"${RUN_DIR}/task-buffered.json"'");console.log(JSON.stringify({status:j.task.status,events:j.events.length,turns:j.turns.length}))'

log "7/7 汇总"
api GET /api/sessions > "${RUN_DIR}/sessions.json"
api GET /api/tasks > "${RUN_DIR}/tasks.json"
node -e 'const t=require("'"${RUN_DIR}/tasks.json"'").tasks;console.log(JSON.stringify(t.map(x=>({id:x.id,status:x.status,title:x.title})),null,2))'
log "P3 联调通过。产物: ${RUN_DIR}"
