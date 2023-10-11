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
		dir := filepath.Join(fts.LocalDir, sitePrefix, "system", "bluge", resource)
		writer, err := bluge.OpenWriter(bluge.DefaultConfig(dir))
		if err != nil {
			return err
		}
		defer writer.Close()
		err = writer.Update(bluge.Identifier("key"), &bluge.Document{
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
	switch fts.Dialect {
	case "sqlite":
		sq.ExecContext(ctx, fts.DB, sq.CustomQuery{
			Dialect: fts.Dialect,
			Format:  "INSERT INTO ",
		})
	}
	return nil
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
	return keys, err
}

func (fts *FTS) Delete(ctx context.Context, sitePrefix, resource, keys []string) error {
	return nil
}
