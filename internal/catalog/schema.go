package catalog

// schemaV4 is the DDL for schema version 4, run once on an empty database.
//
// taken_at is the capture wall clock where the photo was taken (local
// time), taken_at_offset its UTC offset when known. taken_at_home is the
// same instant as a wall clock in the library's home zone (meta
// home_zone; see docs/design/home-time.md), or taken_at when the offset
// is unknown: the timeline orders, groups and filters by it.
// taken_at_offset_source says where the offset came from: 'exif'
// (OffsetTime* tags), 'video' (QuickTime creation date with offset),
// 'gps' (reserved: a time zone looked up from coordinates), 'nearby'
// (estimated from a photo of the same camera taken around the same
// instant), 'home' (an instant shown in the home zone, the local offset
// being unknown); NULL when the offset is unknown.
const schemaV4 = `
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
    taken_at_home     INTEGER,
    taken_at_offset_source TEXT
                      CHECK (taken_at_offset_source IN ('exif','video','gps','nearby','home')),
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
CREATE INDEX IF NOT EXISTS media_taken_at_home ON media(taken_at_home);
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

// migrateV3 adds the version 4 columns to a version 3 catalog; the values
// are filled in by migrateV3Data in the same transaction.
const migrateV3 = `
ALTER TABLE media ADD COLUMN taken_at_home INTEGER;
ALTER TABLE media ADD COLUMN taken_at_offset_source TEXT
    CHECK (taken_at_offset_source IN ('exif','video','gps','nearby','home'));
CREATE INDEX IF NOT EXISTS media_taken_at_home ON media(taken_at_home);
`

// schemaV3 is the DDL of schema version 3 (migrated in place to version
// 4 on open; kept for tests that build a version 3 catalog).
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

// derivedTables are tables added after schema version 3 that hold only
// derived data (near-duplicate pairs, bookkeeping). They are created on
// open when absent, with no version bump: a catalog without them is a
// valid v3 catalog, and rebuild recreates them empty.
const derivedTables = `
CREATE TABLE IF NOT EXISTS similar_pairs (
    hash_a   INTEGER NOT NULL,
    hash_b   INTEGER NOT NULL,
    distance INTEGER NOT NULL,
    PRIMARY KEY (hash_a, hash_b)
);
CREATE INDEX IF NOT EXISTS similar_pairs_b ON similar_pairs(hash_b);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`
