package hub

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  hostname TEXT NOT NULL,
  os TEXT NOT NULL,
  arch TEXT NOT NULL,
  role TEXT NOT NULL,
  boot_id TEXT NOT NULL,
  capabilities TEXT NOT NULL,
  edge_version TEXT NOT NULL,
  protocol_version TEXT NOT NULL,
  secret_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_seen_at TEXT,
  last_metrics_at TEXT,
  revoked_at TEXT
);

CREATE TABLE IF NOT EXISTS pairing_tokens (
  token_hash TEXT PRIMARY KEY,
  label TEXT,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at TEXT,
  used_by_device TEXT
);

CREATE TABLE IF NOT EXISTS mobile_pairing_codes (
  code_hash TEXT PRIMARY KEY,
  label TEXT,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at TEXT,
  used_credential_id TEXT
);

-- revoked_at 是早期「软撤销」留下的列：现在撤销直接 DELETE 行，代码不再写它。
-- 列保留是因为 openDB 的清理语句要按它筛历史数据（新库这一列永远是 NULL）。
CREATE TABLE IF NOT EXISTS mobile_credentials (
  id TEXT PRIMARY KEY,
  secret_hash TEXT NOT NULL,
  previous_secret_hash TEXT,
  previous_valid_until TEXT,
  label TEXT NOT NULL,
  platform TEXT NOT NULL,
  model TEXT NOT NULL,
  app_version TEXT NOT NULL,
  scopes TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_used_at TEXT,
  expires_at TEXT NOT NULL,
  revoked_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_mobile_credentials_created ON mobile_credentials(created_at);

CREATE TABLE IF NOT EXISTS metrics (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id TEXT NOT NULL REFERENCES devices(id),
  sampled_at TEXT NOT NULL,
  received_at TEXT NOT NULL,
  cpu_pct REAL,
  mem_used_bytes INTEGER,
  mem_total_bytes INTEGER,
  disk_used_bytes INTEGER,
  disk_total_bytes INTEGER,
  gpu_util_pct REAL,
  gpu_mem_used_bytes INTEGER,
  gpu_mem_total_bytes INTEGER,
  gpu_temp_c REAL
);

CREATE INDEX IF NOT EXISTS idx_metrics_device_sampled ON metrics(device_id, sampled_at);
CREATE INDEX IF NOT EXISTS idx_metrics_device_id ON metrics(device_id, id);

CREATE TABLE IF NOT EXISTS web_sessions (
  id_hash TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  source TEXT NOT NULL,
  device_id TEXT NOT NULL,
  session_id TEXT,
  status TEXT NOT NULL,
  stage TEXT,
  cwd TEXT NOT NULL,
  request TEXT NOT NULL,
  idempotency_key TEXT,
  evidence_kind TEXT,
  evidence_note TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_idempotency ON tasks(idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  mode TEXT NOT NULL,
  status TEXT NOT NULL,
  cwd TEXT NOT NULL,
  harness TEXT,
  harness_version TEXT,
  harness_session_id TEXT,
  created_at TEXT NOT NULL,
  last_activity_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS turns (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  request TEXT NOT NULL,
  status TEXT NOT NULL,
  started_at TEXT,
  ended_at TEXT,
  exit_code INTEGER,
  result TEXT,
  error TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS commands (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  session_id TEXT,
  turn_id TEXT,
  device_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  payload TEXT NOT NULL,
  error TEXT,
  created_at TEXT NOT NULL,
  delivered_at TEXT,
  acked_at TEXT,
  expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_commands_device_status ON commands(device_id, status);
CREATE INDEX IF NOT EXISTS idx_commands_task ON commands(task_id);

CREATE TABLE IF NOT EXISTS task_events (
  event_id TEXT PRIMARY KEY,
  task_id TEXT,
  session_id TEXT,
  turn_id TEXT,
  device_id TEXT NOT NULL,
  type TEXT NOT NULL,
  payload TEXT NOT NULL,
  at TEXT NOT NULL,
  received_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_task_events_task ON task_events(task_id, at);

CREATE TABLE IF NOT EXISTS artifacts (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  device_id TEXT NOT NULL,
  turn_id TEXT,
  name TEXT NOT NULL,
  path TEXT NOT NULL,
  mime TEXT NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  content BLOB NOT NULL,
  created_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_artifacts_task_name_sha ON artifacts(task_id, name, sha256);
CREATE INDEX IF NOT EXISTS idx_artifacts_task ON artifacts(task_id, created_at);

CREATE TABLE IF NOT EXISTS device_harness (
  device_id TEXT PRIMARY KEY REFERENCES devices(id),
  url TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS device_dsh (
  device_id TEXT PRIMARY KEY REFERENCES devices(id),
  profile TEXT NOT NULL,
  mode TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS device_dsh_state (
  device_id TEXT PRIMARY KEY REFERENCES devices(id),
  payload TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS attachments (
  id TEXT PRIMARY KEY,
  task_id TEXT,
  turn_id TEXT,
  device_id TEXT,
  name TEXT NOT NULL,
  mime TEXT NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  content BLOB NOT NULL,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_attachments_task ON attachments(task_id, created_at);

CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT NOT NULL,
  device_id TEXT,
  type TEXT NOT NULL,
  detail TEXT
);

CREATE TABLE IF NOT EXISTS notification_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  enabled INTEGER NOT NULL DEFAULT 1,
  quiet_hours_enabled INTEGER NOT NULL DEFAULT 1,
  quiet_start TEXT NOT NULL DEFAULT '22:00',
  quiet_end TEXT NOT NULL DEFAULT '08:00',
  updated_at TEXT NOT NULL
);

`

func nowISO(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func openDB(dbPath string) (*sql.DB, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	for _, statement := range strings.Split(schema, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	// 回合通知不再落库（用户要求不保留历史），旧库里的 turn_notifications 表
	// 连同历史记录一并删掉。旧数据不可恢复——这张表只存过通知历史，没有别的消费者。
	if _, err := db.Exec("DROP TABLE IF EXISTS turn_notifications"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("drop turn_notifications: %w", err)
	}
	// 移动设备撤销从软撤销（revoked_at）改成硬删除。旧库里还留着当年软撤销的行，
	// 这些行不再被任何逻辑使用，一次性清掉。旧数据不可恢复——撤销事件在 events 表里有记录。
	if _, err := db.Exec("DELETE FROM mobile_credentials WHERE revoked_at IS NOT NULL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("purge revoked mobile credentials: %w", err)
	}
	return db, nil
}
