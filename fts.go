package nb7

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/blugelabs/bluge"
)

type FTS struct {
	// TODO: Once mattn/sqlite3 and modernc.org/sqlite update their SQLite
	// version up to 3.43.1 (2023-09-11), we can implement full text search
	// built on SQLite's native FTS5 extension. We really want to use FTS
	// contentless-delete tables and not just FTS contentless tables in order
	// to avoid the pitfalls of the 'delete' command as documented in
	// https://www.sqlite.org/fts5.html#the_delete_command (namely we need to
	// provide the exact same content to the delete command or the database
	// will become corrupted with 'database disk image is malformed'). We do
	// NOT want to keep track of the old values just to delete it from the
	// index, ideally we just need to provide the rowid in order to delete an
	// entry (that's what contentless-delete tables offer).
	LocalDir string
}

func (fts *FTS) Setup() error {
	return nil
}

func (fts *FTS) Index(ctx context.Context, sitePrefix, resource, key, value string) error {
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

func (fts *FTS) Match(ctx context.Context, sitePrefix, resource, term string) (keys []string, err error) {
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

func (fts *FTS) Delete(ctx context.Context, sitePrefix, resource string, keys []string) error {
	dir := filepath.Join(fts.LocalDir, sitePrefix, "system", "bluge", resource)
	writer, err := bluge.OpenWriter(bluge.DefaultConfig(dir))
	if err != nil {
		return err
	}
	writer.Delete()
	return nil
}
