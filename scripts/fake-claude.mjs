#!/usr/bin/env node
// 伪造的 Claude CLI：输出与 --output-format stream-json 兼容的行，用于 P3 联调。
// 支持: -p <prompt> --output-format stream-json --verbose [--resume <id>]
// 环境变量 FAKE_CLAUDE_DELAY_MS 控制耗时（默认 1500ms）；prompt 含 SLOW 时默认 30s。
const args = process.argv.slice(2);
const promptIndex = args.indexOf("-p");
const prompt = promptIndex >= 0 ? String(args[promptIndex + 1] ?? "") : "";
const resumeIndex = args.indexOf("--resume");
const resume = resumeIndex >= 0 ? String(args[resumeIndex + 1] ?? "") : null;
const sessionId = resume || "claude_stub_" + Math.random().toString(16).slice(2, 10);

const envDelay = Number(process.env.FAKE_CLAUDE_DELAY_MS ?? "0");
const promptDelay = Number(prompt.match(/DELAY(\d+)/)?.[1] ?? "0");
const delay =
  Number.isFinite(envDelay) && envDelay > 0
    ? envDelay
    : Number.isFinite(promptDelay) && promptDelay > 0
      ? promptDelay
      : prompt.includes("SLOW")
        ? 30000
        : 1500;

function emit(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

emit({
  type: "system",
  subtype: "init",
  session_id: sessionId,
  model: "fake-claude",
  cwd: process.cwd(),
  tools: [],
  permissionMode: "default",
  slash_commands: [],
  apiKeySource: "none",
});
emit({
  type: "assistant",
  message: {
    role: "assistant",
    content: [{ type: "text", text: "STUB_ACK: " + prompt.slice(0, 80) }],
  },
  session_id: sessionId,
});
emit({
  type: "user",
  message: { role: "user", content: [{ type: "text", text: "tool: fake noop" }] },
  session_id: sessionId,
});

let finished = false;
function finish(exitCode, cancelled) {
  if (finished) return;
  finished = true;
  if (cancelled) {
    emit({
      type: "result",
      subtype: "error",
      is_error: true,
      session_id: sessionId,
      result: "cancelled",
      duration_ms: delay,
      total_cost_usd: 0,
    });
    process.exit(exitCode);
  }
  emit({
    type: "result",
    subtype: "success",
    is_error: false,
    session_id: sessionId,
    result: "STUB_OK: " + prompt.slice(0, 80),
    duration_ms: delay,
    total_cost_usd: 0,
    num_turns: 1,
  });
  process.exit(exitCode);
}

const timer = setTimeout(() => finish(0, false), delay);
process.on("SIGTERM", () => {
  clearTimeout(timer);
  finish(143, true);
});
process.on("SIGINT", () => {
  clearTimeout(timer);
  finish(130, true);
});