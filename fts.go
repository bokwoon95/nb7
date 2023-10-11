package nb7

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/blugelabs/bluge"
	"github.com/bokwoon95/sq"
)

type FTS struct {
	LocalDir string
	DB       *sql.DB
	Dialect  string
}

func (fts *FTS) Index(ctx context.Context, sitePrefix, resource, key, value string) error {
	if fts.DB == nil || (fts.LocalDir != "" && (resource == "notes" || resource == "pages" || resource == "posts" || resource == "themes")) {
		// TODO: consider persisting to the DB if it is present (and isn't
		// SQLite) because that users are less likely to fuck up their data by
		// copying bluge index files in an incomplete state.
		dir := filepath.Join(fts.LocalDir, sitePrefix, "system", "bluge", resource)
		writer, err := bluge.OpenWriter(bluge.DefaultConfig(dir))
		if err != nil {
			return err
		}
		defer writer.Close()
		err = writer.Update(bluge.Identifier("key"), &bluge.Document{
			bluge.NewKeywordField("key", key).StoreValue().Sortable(), // TODO: note sure if this is needed, remove it and try if it still works.
			bluge.NewTextField("value", value),
		})
		if err != nil {
			return err
		}
		err = writer.Close()
		if err != nil {
			return err
		}
		return nil
	}
	if resource == "journal" {
		journalEntryID, err := base32Encoding.DecodeString(key)
		if err != nil {
		}
		_ = journalEntryID // TODO: is this how you derive the UUID from the journal entry key? Does base32 map cleanly to 5 + 11 bytes?
	}
	switch fts.Dialect {
	case "sqlite":
		if resource == "journal" {
			_, err := sq.ExecContext(ctx, fts.DB, sq.CustomQuery{
				Dialect: fts.Dialect,
				Format: "INSERT INTO journal_entry_index (rowid, value)" +
					" VALUES ((SELECT rowid FROM journal_entry WHERE journal_entry_id = {journalEntryID}), {value})" +
					" ON CONFLICT DO UPDATE SET value = EXCLUDED.value WHERE rowid = EXCLUDED.rowid",
				Values: []any{
					sq.UUIDParam("journalEntryID", nil),
					sq.StringParam("value", value),
				},
			})
			if err != nil {
				return err
			}
			return nil
		}
		tx, err := fts.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, err = sq.ExecContext(ctx, tx, sq.CustomQuery{
			Dialect: fts.Dialect,
			Format: "INSERT INTO files (site_prefix, resource, key)" +
				" VALUES ({sitePrefix}, {resource}, {key})" +
				" ON CONFLICT DO NOTHING",
			Values: []any{
				sq.StringParam("sitePrefix", sitePrefix),
				sq.StringParam("resource", resource),
				sq.StringParam("key", key),
			},
		})
		if err != nil {
			return err
		}
		_, err = sq.ExecContext(ctx, tx, sq.CustomQuery{
			Dialect: fts.Dialect,
			Format: "INSERT INTO files_index (rowid, value)" +
				" VALUES ((SELECT rowid FROM files WHERE site_prefix = {sitePrefix} AND resource = {resource} AND key = {key}), {value})" +
				" ON CONFLICT DO NOTHING",
			Values: []any{
				sq.StringParam("sitePrefix", sitePrefix),
				sq.StringParam("resource", resource),
				sq.StringParam("key", key),
				sq.StringParam("value", value),
			},
		})
		if err != nil {
			return err
		}
		err = tx.Commit()
		if err != nil {
			return err
		}
		return nil
	case "postgres":
		return nil // TODO:
	case "mysql":
		return nil // TODO:
	default:
		return fmt.Errorf("unknown database dialect %q", fts.Dialect)
	}
}

func (fts *FTS) Match(ctx context.Context, sitePrefix, resource, term string) (keys []string, err error) {
	if fts.DB == nil || (fts.LocalDir != "" && (resource == "notes" || resource == "pages" || resource == "posts" || resource == "themes")) {
		dir := filepath.Join(fts.LocalDir, sitePrefix, "system", "bluge", resource)
		reader, err := bluge.OpenReader(bluge.DefaultConfig(dir))
		if err != nil {
			return nil, fmt.Errorf("open reader: %w", err)
		}
		defer reader.Close()
		query := bluge.NewMatchQuery(term).SetField("value")
		documentMatchIterator, err := reader.Search(context.Background(), bluge.NewAllMatches(query))
		if err != nil {
			return nil, err
		}
		for {
			match, err := documentMatchIterator.Next()
			if err != nil {
				return nil, err
			}
			if match == nil {
				break
			}
			err = match.VisitStoredFields(func(field string, value []byte) bool {
				if field == "key" {
					keys = append(keys, string(value))
					return false
				}
				return true
			})
			if err != nil {
				return nil, err
			}
		}
		return keys, nil
	}
	switch fts.Dialect {
	case "sqlite":
		if resource == "journal" {
			return keys, nil
		}
		sq.ExecContext(ctx, fts.DB, sq.CustomQuery{
			Dialect: fts.Dialect,
			Format:  "SELECT key FROM ",
		})
		return keys, nil
	case "postgres":
		return keys, nil // TODO:
	case "mysql":
		return keys, nil // TODO:
	default:
		return nil, fmt.Errorf("unknown database dialect %q", fts.Dialect)
	}
}

func (fts *FTS) Delete(ctx context.Context, sitePrefix, resource, keys []string) error {
	return nil
}
