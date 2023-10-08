CREATE VIRTUAL TABLE journal_entry_fts5 USING FTS5 (
    content
    ,content='journal_entry'
    ,content_rowid='journal_entry_id'
);

CREATE TRIGGER journal_entry_fts5_after_insert_trg AFTER INSERT ON journal_entry BEGIN
    INSERT INTO journal_entry_fts5 (ROWID, content) VALUES (NEW.journal_entry_id, NEW.content);
END;

CREATE TRIGGER journal_entry_fts5_after_delete_trg AFTER DELETE ON journal_entry BEGIN
    INSERT INTO journal_entry_fts5 (journal_entry_fts5, ROWID, content) VALUES ('delete', OLD.journal_entry_id, OLD.content);
END;

CREATE TRIGGER journal_entry_fts5_after_update_trg AFTER UPDATE ON journal_entry BEGIN
    INSERT INTO journal_entry_fts5 (journal_entry_fts5, ROWID, content) VALUES ('delete', OLD.journal_entry_id, OLD.content);
    INSERT INTO journal_entry_fts5 (ROWID, content) VALUES (NEW.journal_entry_id, NEW.content);
END;
