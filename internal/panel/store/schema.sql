-- starport-panel 数据库 schema。全部 IF NOT EXISTS，可重复执行；结构变更走 migrations（见 store.go）。

CREATE TABLE IF NOT EXISTS nodes (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    hostname         TEXT    NOT NULL DEFAULT '',
    internal_ip      TEXT    NOT NULL DEFAULT '',
    os               TEXT    NOT NULL DEFAULT '',
    arch             TEXT    NOT NULL DEFAULT '',
    kernel           TEXT    NOT NULL DEFAULT '',
    cpu_cores        INTEGER NOT NULL DEFAULT 0,
    mem_bytes        INTEGER NOT NULL DEFAULT 0,
    cpu_used_percent REAL    NOT NULL DEFAULT 0,
    mem_used_percent REAL    NOT NULL DEFAULT 0,
    agent_version    TEXT    NOT NULL DEFAULT '',
    agent_token      TEXT    NOT NULL UNIQUE,
    online           INTEGER NOT NULL DEFAULT 0,
    registered_at    TEXT    NOT NULL,
    last_seen_at     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_nodes_host_ip ON nodes(hostname, internal_ip);

CREATE TABLE IF NOT EXISTS clusters (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    name                   TEXT    NOT NULL UNIQUE,
    k8s_version            TEXT    NOT NULL,
    pod_cidr               TEXT    NOT NULL,
    service_cidr           TEXT    NOT NULL,
    control_plane_endpoint TEXT    NOT NULL DEFAULT '',
    vip                    TEXT    NOT NULL DEFAULT '',
    vip_interface          TEXT    NOT NULL DEFAULT '',
    cni                    TEXT    NOT NULL,
    cni_version            TEXT    NOT NULL DEFAULT '',
    addons                 TEXT    NOT NULL DEFAULT '[]',   -- JSON 数组
    artifact_mode          TEXT    NOT NULL,
    bundle_url             TEXT    NOT NULL DEFAULT '',
    use_cn_mirror          INTEGER NOT NULL DEFAULT 0,
    status                 TEXT    NOT NULL,                -- created | installing | ready | failed
    kubeconfig             TEXT    NOT NULL DEFAULT '',
    join_token             TEXT    NOT NULL DEFAULT '',
    join_ca_cert_hash      TEXT    NOT NULL DEFAULT '',
    join_certificate_key   TEXT    NOT NULL DEFAULT '',
    join_issued_at         TEXT    NOT NULL DEFAULT '',
    created_at             TEXT    NOT NULL,
    updated_at             TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS cluster_nodes (
    cluster_id  INTEGER NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    node_id     INTEGER NOT NULL REFERENCES nodes(id),
    role        TEXT    NOT NULL,                -- first-master | join-master | worker
    status      TEXT    NOT NULL,                -- installing | ready | failed
    error       TEXT    NOT NULL DEFAULT '',
    task_id     INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT    NOT NULL,
    PRIMARY KEY (cluster_id, node_id)
);

CREATE TABLE IF NOT EXISTS tasks (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    kind          TEXT    NOT NULL,              -- install | exec
    node_id       INTEGER NOT NULL,
    cluster_id    INTEGER NOT NULL DEFAULT 0,
    status        TEXT    NOT NULL,              -- running | succeeded | failed | cancelled
    exit_code     INTEGER NOT NULL DEFAULT 0,
    error_code    TEXT    NOT NULL DEFAULT '',
    error_message TEXT    NOT NULL DEFAULT '',
    started_at    TEXT    NOT NULL,
    finished_at   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_tasks_node ON tasks(node_id, id);

CREATE TABLE IF NOT EXISTS api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    prefix       TEXT    NOT NULL,                -- 明文前 12 字符，用于人眼识别
    hash         TEXT    NOT NULL UNIQUE,         -- sha256(明文) hex
    created_at   TEXT    NOT NULL,
    last_used_at TEXT    NOT NULL DEFAULT '',
    revoked_at   TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS task_logs (
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    seq     INTEGER NOT NULL,
    at      TEXT    NOT NULL,
    line    TEXT    NOT NULL,
    PRIMARY KEY (task_id, seq)
);
