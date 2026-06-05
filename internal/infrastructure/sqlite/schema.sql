-- One event = one database file. Mirrors the run5 model subset defined in
-- docs/event-export-format.md. All *_ms columns are unix milliseconds.

CREATE TABLE IF NOT EXISTS events (
    id   TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL DEFAULT '',
    date TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS laps (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS races (
    id                              TEXT PRIMARY KEY,
    event_id                        TEXT NOT NULL REFERENCES events(id),
    name                            TEXT NOT NULL,
    date                            TEXT NOT NULL DEFAULT '',
    started_at_ms                   INTEGER,
    lap_id                          TEXT REFERENCES laps(id),
    format                          TEXT NOT NULL DEFAULT 'FixedDistance',
    time_limit_seconds              INTEGER,
    category_excludes_top_by_gender INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS categories (
    id     TEXT PRIMARY KEY,
    name   TEXT NOT NULL,
    min    INTEGER,
    max    INTEGER,
    gender TEXT
);

CREATE TABLE IF NOT EXISTS checkpoints (
    id                       TEXT PRIMARY KEY,
    event_id                 TEXT NOT NULL REFERENCES events(id),
    race_id                  TEXT NOT NULL REFERENCES races(id),
    name                     TEXT NOT NULL DEFAULT '',
    type                     INTEGER NOT NULL,
    sort                     INTEGER NOT NULL DEFAULT 0,
    board                    TEXT NOT NULL,
    since_ms                 INTEGER,
    since_offset_seconds     INTEGER,
    sleep_after_prev_seconds INTEGER
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_race_board ON checkpoints(race_id, board, sort);

CREATE TABLE IF NOT EXISTS members (
    id             TEXT PRIMARY KEY,
    event_id       TEXT NOT NULL REFERENCES events(id),
    race_id        TEXT NOT NULL REFERENCES races(id),
    category_id    TEXT REFERENCES categories(id),
    number         INTEGER,
    epc            TEXT,
    rfid           TEXT,
    first_name     TEXT NOT NULL DEFAULT '',
    last_name      TEXT NOT NULL DEFAULT '',
    gender         TEXT,
    dob            TEXT,
    city           TEXT,
    team           TEXT,
    status         INTEGER NOT NULL DEFAULT 0,
    start_time_ms  INTEGER,
    finish_time_ms INTEGER,
    clean_time     TEXT
);

CREATE INDEX IF NOT EXISTS idx_members_event_epc ON members(event_id, epc);
CREATE INDEX IF NOT EXISTS idx_members_event_number ON members(event_id, number);
CREATE INDEX IF NOT EXISTS idx_members_race ON members(race_id);

CREATE TABLE IF NOT EXISTS rfid_logs (
    id          TEXT PRIMARY KEY, -- md5(board+epc+timeMs+ant), see architecture.md
    event_id    TEXT NOT NULL REFERENCES events(id),
    status      INTEGER NOT NULL DEFAULT 0,
    number      INTEGER NOT NULL DEFAULT 0,
    time_ms     INTEGER NOT NULL,
    ant         INTEGER NOT NULL DEFAULT 0,
    epc         TEXT NOT NULL DEFAULT '',
    rssi        INTEGER NOT NULL DEFAULT 0,
    board       TEXT NOT NULL,
    disabled_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_rfid_logs_time ON rfid_logs(event_id, time_ms);
CREATE INDEX IF NOT EXISTS idx_rfid_logs_board ON rfid_logs(board);

CREATE TABLE IF NOT EXISTS results (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id      TEXT NOT NULL REFERENCES events(id),
    race_id       TEXT NOT NULL REFERENCES races(id),
    member_id     TEXT NOT NULL REFERENCES members(id),
    checkpoint_id TEXT NOT NULL REFERENCES checkpoints(id),
    rfid_log_id   TEXT REFERENCES rfid_logs(id),
    time_ms       INTEGER NOT NULL,
    number        INTEGER
);

-- One derived result per rfid log (multiple NULLs allowed for manual results).
CREATE UNIQUE INDEX IF NOT EXISTS uq_results_rfid_log ON results(rfid_log_id);
CREATE INDEX IF NOT EXISTS idx_results_member_time ON results(member_id, race_id, time_ms);
