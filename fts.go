package nb7

import (
	"database/sql"
	"strings"
)

type FTS interface {
	Index(contentID, content string) error
	Match(term string) (contentIDs []string, err error)
	Delete(contentID string) error
	DeleteAll() error
}

func parseContentID(contentID string) (sitePrefix, resource string) {
	head, tail, _ := strings.Cut(contentID, "/")
	if strings.HasPrefix(head, "@") || strings.Contains(head, ".") {
		sitePrefix = head
		head, tail, _ = strings.Cut(tail, "/")
	}
	switch head {
	case "journal", "notes", "pages", "posts", "themes":
		resource = head
		return sitePrefix, resource
	default:
		return "", ""
	}
}

type BlugeFTS struct {
	LocalDir string
}

func (blugeFTS *BlugeFTS) Index(contentID, content string) error {
	return nil
}

func (blugFTS *BlugeFTS) Match(term string) (contentIDs []string, err error) {
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
