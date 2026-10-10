package catalog

// schemaV3 is the DDL for schema version 3, run once on an empty database.
const schemaV3 = `
CREATE TABLE IF NOT EXISTS media (
    id                INTEGER PRIMARY KEY,
    hash              INTEGER NOT NULL UNIQUE,
    size              INTEGER NOT NULL,
    rel_path          TEXT NOT NULL UNIQUE COLLATE NOCASE,
    file_type         TEXT NOT NULL,
    ext               TEXT NOT NULL,
    taken_at          INTEGER,
    taken_at_offset   INTEGER,
    taken_at_source   TEXT NOT NULL
                      CHECK (taken_at_source IN ('exif','video','video_utc','mtime','none')),
    width             INTEGER,
    height            INTEGER,
    orientation       INTEGER,
    duration_ms       INTEGER,
    lat               REAL,
    lon               REAL,
    altitude          REAL,
    camera_make       TEXT NOT NULL DEFAULT '',
    camera_model      TEXT NOT NULL DEFAULT '',
    camera_type       TEXT NOT NULL DEFAULT '',
    lens              TEXT NOT NULL DEFAULT '',
    original_path     TEXT NOT NULL DEFAULT '',
    original_mtime    INTEGER,
    imported_at       INTEGER NOT NULL,
    import_session_id INTEGER,
    rating            INTEGER NOT NULL DEFAULT 0,
    deleted_at        INTEGER,
    phash             INTEGER,
    thumb_state       TEXT NOT NULL DEFAULT 'pending'
                      CHECK (thumb_state IN ('pending','ok','unsupported','error','stale')),
    thumb_error       TEXT NOT NULL DEFAULT '',
    thumb_at          INTEGER
);
CREATE INDEX IF NOT EXISTS media_taken_at ON media(taken_at);
CREATE INDEX IF NOT EXISTS media_file_type ON media(file_type);
CREATE INDEX IF NOT EXISTS media_latlon ON media(lat, lon) WHERE lat IS NOT NULL;
CREATE INDEX IF NOT EXISTS media_session ON media(import_session_id);
CREATE INDEX IF NOT EXISTS media_thumb_state ON media(thumb_state);
CREATE INDEX IF NOT EXISTS media_phash ON media(phash);

CREATE TABLE IF NOT EXISTS import_sessions (
    id            INTEGER PRIMARY KEY,
    started_at    INTEGER NOT NULL,
    finished_at   INTEGER,
    source        TEXT NOT NULL,
    mode          TEXT NOT NULL,
    imported      INTEGER NOT NULL DEFAULT 0,
    existing      INTEGER NOT NULL DEFAULT 0,
    duplicates    INTEGER NOT NULL DEFAULT 0,
    skipped_small INTEGER NOT NULL DEFAULT 0,
    non_media     INTEGER NOT NULL DEFAULT 0,
    errors        INTEGER NOT NULL DEFAULT 0
);
`
