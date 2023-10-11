-- SQLite
CREATE TABLE names (
    name TEXT PRIMAKY KEY NOT NULL
);

CREATE VIRTUAL_TABLE content_fts5 USING fts5 (
    content
);

CREATE VIRTUAL TABLE journal_entry_fts5 USING FTS5 (
    content
    ,content='journal_entry'
    ,content_rowid='journal_entry_id'
);

-- journal_entry
-- notes
-- posts
-- pages
-- themes

-- Postgres

-- MySQL
