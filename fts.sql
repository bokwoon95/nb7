-- SQLite
CREATE TABLE files (
    site_prefix TEXT
    ,resource TEXT
    ,key TEXT

    ,CONSTRAINT files_site_prefix_resource_key_pkey PRIMARY KEY (site_prefix, resource, key)
);

CREATE VIRTUAL TABLE files_index USING fts5 (value, content = ''/*, contentless_delete = 1 */);

CREATE VIRTUAL TABLE journal_entry_index USING fts5 (value, content = 'journal_entry'/* , contentless_delete = 1 */);

-- Postgres
CREATE TABLE files_index (
    site_prefix VARCHAR(500)
    ,resource VARCHAR(500)
    ,key VARCHAR(500)
    ,ts TSVECTOR

    ,CONSTRAINT files_index_site_prefix_resource_key_pkey PRIMARY KEY (site_prefix, resource, key)
);

CREATE INDEX files_index_ts_idx ON files_index USING GIN (ts);

ALTER TABLE journal_entry ADD COLUMN ts TSVECTOR;

CREATE INDEX journal_entry_ts_idx ON journal_entry USING GIN (ts);

-- MySQL
CREATE TABLE files_index (
    site_prefix VARCHAR(500)
    ,resource VARCHAR(500)
    ,key VARCHAR(500)
    ,value MEDIUMTEXT

    ,CONSTRAINT files_index_site_prefix_resource_key_pkey PRIMARY KEY (site_prefix, resource, key)
);

CREATE FULLTEXT INDEX files_index_value_idx ON files_index (value);

CREATE FULLTEXT INDEX journal_entry_value_idx ON journal_entry (value);
