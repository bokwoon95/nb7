package nb7

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/blugelabs/bluge"
)

type FTS struct {
	LocalDir string
	DB       *sql.DB
	Dialect  string
}

func (fts *FTS) Index(sitePrefix, resource, name, content string) error {
	return nil
}

func (fts *FTS) Match(sitePrefix, resource, term string) (names []string, err error) {
	return names, err
}

func (fts *FTS) Delete(sitePrefix, resource, contentID string) error {
	return nil
}

func (fts *FTS) DeleteAll(sitePrefix, resource string) error {
	return nil
}

type BlugeFTS struct {
	LocalDir string
}

func (blugeFTS *BlugeFTS) Index(name, content string) error {
	var sitePrefix, resource string
	segments := strings.Split(name, "/")
	if strings.HasPrefix(segments[0], "@") || strings.Contains(segments[0], ".") {
		sitePrefix = segments[0]
		if len(segments) > 1 {
			resource = segments[1]
		}
	} else {
		resource = segments[0]
	}
	switch resource {
	case "journal", "notes", "pages", "posts", "themes":
		break
	default:
		return fmt.Errorf("invalid name %q", name)
	}
	config := bluge.DefaultConfig(filepath.Join(blugeFTS.LocalDir, sitePrefix, "system", "bluge", resource))
	writer, err := bluge.OpenWriter(config)
	if err != nil {
		return fmt.Errorf("open writer: %w", err)
	}
	defer writer.Close()
	nameField := bluge.NewKeywordField("name", name)
	nameField.StoreValue() // no idea what this does, copied from bluge.NewDocument
	nameField.Sortable()   // no idea what this does, copied from bluge.NewDocument
	contentField := bluge.NewTextField("content", content)
	err = writer.Update(bluge.Identifier(nameField.Value()), &bluge.Document{
		nameField,
		contentField,
	})
	if err != nil {
		return fmt.Errorf("update writer: %w", err)
	}
	err = writer.Close()
	if err != nil {
		return fmt.Errorf("close writer: %w", err)
	}
	return nil
}

func (blugeFTS *BlugeFTS) Match(term string) (contentIDs []string, err error) {
	return contentIDs, nil
}

type SQLiteFTS struct {
	DB *sql.DB
}

func (sqliteFTS *SQLiteFTS) Index(contentID, content string) error {
	return nil
}

func (sqliteFTS *SQLiteFTS) Match(term string) (contentIDs []string, err error) {
	return contentIDs, nil
}

type PostgresFTS struct {
	DB *sql.DB
}

type MySQLFTS struct {
	DB *sql.DB
}
