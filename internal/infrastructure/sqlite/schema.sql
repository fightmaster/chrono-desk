-- One event = one database file. Mirrors the run5 model subset defined in
-- docs/event-export-format.md. All *_ms columns are unix milliseconds.

CREATE TABLE IF NOT EXISTS events (
    id                    TEXT PRIMARY KEY,
    name                  TEXT NOT NULL,
    slug                  TEXT NOT NULL DEFAULT '',
    date                  TEXT NOT NULL DEFAULT '',
    timezone              TEXT NOT NULL DEFAULT '', -- IANA zone from the export; default for Feibot CSV import
    use_race_date_for_age INTEGER NOT NULL DEFAULT 0
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

-- Which categories are attached to which race (run5's category_race pivot).
-- The catalog (categories) is event-global; this table is what scopes the
-- "available for assignment" set per distance. Without it, the attached set
-- could only be inferred from member.category_id, which cannot represent a
-- category attached to a race but not yet assigned to anyone. Local
-- attach/detach is journaled (entity "race_category") and syncs back to run5.
CREATE TABLE IF NOT EXISTS race_categories (
    race_id     TEXT NOT NULL REFERENCES races(id),
    category_id TEXT NOT NULL REFERENCES categories(id),
    PRIMARY KEY (race_id, category_id)
);

CREATE INDEX IF NOT EXISTS idx_race_categories_race ON race_categories(race_id);

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
    checkpoint_id TEXT REFERENCES checkpoints(id), -- NULL → manual judge entry
    rfid_log_id   TEXT REFERENCES rfid_logs(id),   -- NULL → manual judge entry
    time_ms       INTEGER NOT NULL,
    number        INTEGER
);

-- One derived result per rfid log (multiple NULLs allowed for manual results).
CREATE UNIQUE INDEX IF NOT EXISTS uq_results_rfid_log ON results(rfid_log_id);
CREATE INDEX IF NOT EXISTS idx_results_member_time ON results(member_id, race_id, time_ms);

-- Journal of local (offline) edits. Local edits win over re-imports: after an
-- event export is applied, the journal is replayed on top. It is also the
-- to-sync list for pushing offline changes back to the site (v0.3) and the
-- judge's audit trail. Values are JSON-encoded (null | number | string).
CREATE TABLE IF NOT EXISTS local_changes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    entity     TEXT NOT NULL, -- race | checkpoint | member | rfid_log
    entity_id  TEXT NOT NULL,
    field      TEXT NOT NULL,
    old_value  TEXT NOT NULL,
    new_value  TEXT NOT NULL,
    changed_at INTEGER NOT NULL -- unix ms
);

CREATE INDEX IF NOT EXISTS idx_local_changes_entity ON local_changes(entity, entity_id);

-- «Зафиксировать время»: wall-clock finishes the judge captured before a
-- participant number is known. They persist here so a restart doesn't lose
-- them (the bug: they used to live only in frontend state). Binding a number
-- turns one into a manual result via the existing manual-finish path (which is
-- journaled and synced) and the capture row is deleted. Unbound captures have
-- no run5 representation, so they are intentionally local-only (not journaled).
CREATE TABLE IF NOT EXISTS pending_captures (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id   TEXT NOT NULL REFERENCES events(id),
    time_ms    INTEGER NOT NULL,
    created_at INTEGER NOT NULL -- unix ms
);

-- Finish photos pulled from Chrono Cam phones over the LAN (read-only metadata,
-- orthogonal to the timing path). A judge matches them to a fixed time to confirm
-- a number without chasing the runner. See docs and the phone's live API.
-- photo_sources: registered phones to poll, keyed by the base URL the operator
-- enters; source_id/camera_label/skew are learned from the phone's GET /event.
CREATE TABLE IF NOT EXISTS photo_sources (
    base_url     TEXT PRIMARY KEY,
    event_id     TEXT NOT NULL REFERENCES events(id),
    source_id    TEXT NOT NULL DEFAULT '', -- stable phone id (learned)
    camera_label TEXT NOT NULL DEFAULT '',
    skew_ms      INTEGER NOT NULL DEFAULT 0, -- deskNow - phoneServerTime at last poll
    last_seen_at INTEGER,                     -- unix ms of last successful poll
    enabled      INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_photo_sources_event ON photo_sources(event_id);

-- photos: one row per finish track pulled from a phone. id = source_id+':'+track
-- id (idempotent). time_ms is the finish time corrected to the desk clock by the
-- source's measured skew, so it is directly comparable to rfid_logs/results times.
CREATE TABLE IF NOT EXISTS photos (
    id             TEXT PRIMARY KEY,
    event_id       TEXT NOT NULL REFERENCES events(id),
    source_id      TEXT NOT NULL DEFAULT '',
    camera_label   TEXT NOT NULL DEFAULT '',
    time_ms        INTEGER NOT NULL,            -- skew-corrected finish time (unix ms)
    bib            TEXT NOT NULL DEFAULT '',
    bib_source     TEXT NOT NULL DEFAULT 'none', -- manual | ocr | none
    status         INTEGER NOT NULL DEFAULT 0,   -- 0 raw, 1 reviewed, 2 approved (reserved)
    best_photo_url TEXT NOT NULL DEFAULT '',     -- absolute URL on the phone
    frames_json    TEXT NOT NULL DEFAULT '[]',   -- [{timestamp_epoch_ms,url}], absolute
    fetched_at     INTEGER NOT NULL              -- unix ms when pulled
);

CREATE INDEX IF NOT EXISTS idx_photos_event_time ON photos(event_id, time_ms);
CREATE INDEX IF NOT EXISTS idx_photos_event_bib ON photos(event_id, bib);

-- Per-event sync target for pushing/pulling to the run5 site (v0.3). One row
-- (the event's own id). The token authorizes the run5 sync endpoint; it never
-- leaves the desktop except in the X-SYNC-TOKEN header.
CREATE TABLE IF NOT EXISTS sync_config (
    event_id          TEXT PRIMARY KEY,
    base_url          TEXT NOT NULL DEFAULT '',
    token             TEXT NOT NULL DEFAULT '',
    last_synced_at    INTEGER, -- unix ms of last successful push
    last_payload_hash TEXT     -- sha256 of last pushed payload (skip no-op re-push)
);
