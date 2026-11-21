package hub

const taskPoolSchema = `
CREATE TABLE IF NOT EXISTS task_pool_items (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  request TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('unassigned', 'processing', 'blocked', 'review', 'completed', 'cancelled')),
  assignee_kind TEXT NOT NULL DEFAULT '',
  assignee_id TEXT NOT NULL DEFAULT '',
  assignee_actor_id TEXT NOT NULL DEFAULT '',
  assignment_rev INTEGER NOT NULL DEFAULT 0,
  revision INTEGER NOT NULL DEFAULT 1,
  created_by TEXT NOT NULL,
  idempotency_key TEXT UNIQUE,
  request_hash TEXT NOT NULL,
  attachment_ids TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_task_pool_actor ON task_pool_items(assignee_actor_id, status);

CREATE TABLE IF NOT EXISTS task_pool_events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id TEXT NOT NULL REFERENCES task_pool_items(id),
  event_id TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  assignment_rev INTEGER NOT NULL,
  type TEXT NOT NULL,
  payload TEXT NOT NULL,
  at TEXT NOT NULL,
  UNIQUE (item_id, assignment_rev, event_id)
);

CREATE TABLE IF NOT EXISTS task_pool_deliveries (
  id TEXT PRIMARY KEY,
  item_id TEXT NOT NULL REFERENCES task_pool_items(id),
  assignment_rev INTEGER NOT NULL,
  purpose TEXT NOT NULL,
  source_event_id TEXT NOT NULL,
  target_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'queued', 'uncertain', 'failed', 'acknowledged', 'superseded')),
  error_code TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT,
  queued_at TEXT,
  UNIQUE (item_id, assignment_rev, purpose, source_event_id)
);

CREATE INDEX IF NOT EXISTS idx_task_pool_delivery_queue ON task_pool_deliveries(status, created_at);

CREATE TABLE IF NOT EXISTS task_pool_runs (
  item_id TEXT NOT NULL REFERENCES task_pool_items(id),
  task_id TEXT NOT NULL REFERENCES tasks(id),
  PRIMARY KEY (item_id, task_id)
);
`
