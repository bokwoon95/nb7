package nb7

import "strings"

type FTS interface {
	Index(contentID, content string) error
	Match(term string) (contentIDs []string, err error)
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
}

type SQLiteFTS struct {
}

type PostgresFTS struct {
}

type MySQLFTS struct {
}
