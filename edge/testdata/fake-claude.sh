#!/bin/sh
# 本机联调用的假 claude harness：输出 Claude Code stream-json 行（不进行真实推理）。
#
# 用法（在 Edge 状态目录 config.json）：
#   {"harness_command": "/绝对路径/edge/testdata/fake-claude.sh"}
# 或：
#   meerkat-edge run --harness-cmd /绝对路径/edge/testdata/fake-claude.sh
#
# 支持：
#   -p <request>          请求文本（本脚本不联网，只回显固定结果）
#   --resume <session_id> 续接时沿用同一个 harness session id
#   --output-format stream-json --verbose  忽略
#   FAKE_CLAUDE_SLEEP=秒  在 assistant 事件后 sleep，便于验证取消/超时
#   FAKE_CLAUDE_FAIL=1    输出 is_error 的 result 事件并以退出码 2 结束
set -eu

resume=""
while [ $# -gt 0 ]; do
  case "$1" in
    --resume)
      resume="${2:-}"
      shift 2
      ;;
    -p)
      shift
      if [ $# -gt 0 ]; then shift; fi
      ;;
    *)
      shift
      ;;
  esac
done

if [ -n "$resume" ]; then
  sid="$resume"
else
  sid="stub-$$-$(date +%s)"
fi

printf '{"type":"system","subtype":"init","session_id":"%s","cwd":".","model":"stub","claude_code_version":"9.9.9-stub","tools":["Bash"]}
' "$sid"
printf '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"stub 已收到请求"}]},"session_id":"%s"}
' "$sid"

if [ "${FAKE_CLAUDE_SLEEP:-0}" != "0" ]; then
  sleep "${FAKE_CLAUDE_SLEEP}"
fi

printf '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_stub_1","name":"Bash","input":{"command":"echo hi"}}]},"session_id":"%s"}
' "$sid"
printf '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_stub_1","content":"hi","is_error":false}]},"session_id":"%s"}
' "$sid"

if [ "${FAKE_CLAUDE_FAIL:-0}" = "1" ]; then
  printf '{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":5,"num_turns":1,"result":"stub 模拟失败","session_id":"%s"}
' "$sid"
  exit 2
fi

printf '{"type":"result","subtype":"success","is_error":false,"duration_ms":5,"num_turns":1,"result":"stub-OK","session_id":"%s"}
' "$sid"
exit 0
