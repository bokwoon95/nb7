package main

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bokwoon95/nb7"
	"github.com/bokwoon95/sq"
)

type PermissionsCmd struct {
	Notebrew *nb7.Notebrew
	Stdout   io.Writer
	Username sql.NullString
	SiteName sql.NullString
	Action   string // grant | revoke | setowner
}

func PermissionsCommand(nbrew *nb7.Notebrew, args ...string) (*PermissionsCmd, error) {
	var cmd PermissionsCmd
	cmd.Notebrew = nbrew
	var grant, revoke bool
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.Func("user", "", func(s string) error {
		cmd.Username = sql.NullString{String: s, Valid: true}
		return nil
	})
	flagset.Func("site", "", func(s string) error {
		cmd.SiteName = sql.NullString{String: s, Valid: true}
		return nil
	})
	flagset.BoolVar(&grant, "grant", false, "")
	flagset.BoolVar(&revoke, "revoke", false, "")
	err := flagset.Parse(args)
	if err != nil {
		return nil, err
	}
	flagArgs := flagset.Args()
	if len(flagArgs) > 0 {
		flagset.Usage()
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(flagArgs, " "))
	}
	if grant && revoke {
		flagset.Usage()
		return nil, fmt.Errorf("-grant and -revoke cannot be provided at the same time")
	}
	if grant {
		cmd.Action = "grant"
	} else if revoke {
		cmd.Action = "revoke"
	}
	return &cmd, nil
}

func (cmd *PermissionsCmd) Run() error {
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if !cmd.Username.Valid && !cmd.SiteName.Valid {
		cursor, err := sq.FetchCursor(cmd.Notebrew.DB, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format: "SELECT {*}" +
				" FROM site_user" +
				" JOIN users ON users.user_id = site_user.user_id" +
				" JOIN site ON site.site_id = site_user.site_id" +
				" ORDER BY users.user_id, site.site_id",
		}, func(row *sq.Row) (result struct {
			UserID   [16]byte
			Username string
			SiteID   [16]byte
			SiteName string
		}) {
			row.UUID(&result.UserID, "users.user_id")
			result.Username = row.String("users.username")
			row.UUID(&result.SiteID, "site.site_id")
			result.SiteName = row.String("site.site_name")
			return result
		})
		if err != nil {
			return err
		}
		defer cursor.Close()
		for cursor.Next() {
			result, err := cursor.Result()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.Stdout, "userid=%s user=%s siteid=%s site=%s\n", hex.EncodeToString(result.UserID[:]), result.Username, hex.EncodeToString(result.SiteID[:]), result.SiteName)
		}
		return cursor.Close()
	}
	if cmd.Username.Valid {
		exists, err := sq.FetchExists(cmd.Notebrew.DB, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format:  "SELECT 1 FROM users WHERE username = {username}",
			Values: []any{
				sq.StringParam("username", cmd.Username.String),
			},
		})
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("user %q does not exist", cmd.Username.String)
		}
		if !cmd.SiteName.Valid {
			cursor, err := sq.FetchCursor(cmd.Notebrew.DB, sq.CustomQuery{
				Dialect: cmd.Notebrew.Dialect,
				Format: "SELECT {*}" +
					" FROM site" +
					" JOIN site_user ON site_user.site_id = site.site_id" +
					" JOIN users ON users.user_id = site_user.user_id" +
					" WHERE users.username = {username}" +
					" ORDER BY site.site_id",
				Values: []any{
					sq.StringParam("username", cmd.Username.String),
				},
			}, func(row *sq.Row) (result struct {
				SiteID   [16]byte
				SiteName string
			}) {
				row.UUID(&result.SiteID, "site.site_id")
				result.SiteName = row.String("site.site_name")
				return result
			})
			if err != nil {
				return err
			}
			defer cursor.Close()
			for cursor.Next() {
				result, err := cursor.Result()
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.Stdout, "siteid=%s site=%s\n", hex.EncodeToString(result.SiteID[:]), result.SiteName)
			}
			return cursor.Close()
		}
	}
	if cmd.SiteName.Valid {
		exists, err := sq.FetchExists(cmd.Notebrew.DB, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format:  "SELECT 1 FROM site WHERE site_name = {siteName}",
			Values: []any{
				sq.StringParam("siteName", cmd.SiteName.String),
			},
		})
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("site %q does not exist", cmd.SiteName.String)
		}
		if !cmd.Username.Valid {
			cursor, err := sq.FetchCursor(cmd.Notebrew.DB, sq.CustomQuery{
				Dialect: cmd.Notebrew.Dialect,
				Format: "SELECT {*}" +
					" FROM users" +
					" JOIN site_user ON site_user.user_id = users.user_id" +
					" JOIN site ON site.site_id = site_user.site_id" +
					" WHERE site.site_name = {siteName}" +
					" ORDER BY users.user_id",
				Values: []any{
					sq.StringParam("siteName", cmd.SiteName.String),
				},
			}, func(row *sq.Row) (result struct {
				UserID   [16]byte
				Username string
			}) {
				row.UUID(&result.UserID, "users.user_id")
				result.Username = row.String("users.username")
				return result
			})
			if err != nil {
				return err
			}
			defer cursor.Close()
			for cursor.Next() {
				result, err := cursor.Result()
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.Stdout, "userid=%s user=%s\n", hex.EncodeToString(result.UserID[:]), result.Username)
			}
			return cursor.Close()
		}
	}
	switch cmd.Action {
	case "grant":
		_, err := sq.Exec(cmd.Notebrew.DB, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format: "INSERT INTO site_user (site_id, user_id)" +
				" VALUES ((SELECT site_id FROM site WHERE site_name = {siteName}), (SELECT user_id FROM users WHERE username = {username}))",
			Values: []any{
				sq.StringParam("username", cmd.Username.String),
				sq.StringParam("siteName", cmd.SiteName.String),
			},
		})
		if err != nil {
			if cmd.Notebrew.IsKeyViolation(err) {
				return nil
			}
			return err
		}
	case "revoke":
		_, err := sq.Exec(cmd.Notebrew.DB, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format: "DELETE FROM site_user" +
				" WHERE site_id = (SELECT site_id FROM site WHERE site_name = {siteName})" +
				" AND user_id = (SELECT user_id FROM users WHERE username = {username})",
			Values: []any{
				sq.StringParam("username", cmd.Username.String),
				sq.StringParam("siteName", cmd.SiteName.String),
			},
		})
		if err != nil {
			return err
		}
	default:
		result, err := sq.FetchOne(cmd.Notebrew.DB, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format: "SELECT {*}" +
				" FROM site_user" +
				" JOIN users ON users.user_id = site_user.user_id" +
				" JOIN site ON site.site_id = site_user.site_id" +
				" WHERE username = {username}" +
				" AND site_name = {siteName}" +
				" ORDER BY users.user_id, site.site_id",
			Values: []any{
				sq.StringParam("username", cmd.Username.String),
				sq.StringParam("siteName", cmd.SiteName.String),
			},
		}, func(row *sq.Row) (result struct {
			UserID   [16]byte
			Username string
			SiteID   [16]byte
			SiteName string
		}) {
			row.UUID(&result.UserID, "users.user_id")
			result.Username = row.String("users.username")
			row.UUID(&result.SiteID, "site.site_id")
			result.SiteName = row.String("site.site_name")
			return result
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		fmt.Fprintf(cmd.Stdout, "userid=%s user=%s siteid=%s site=%s\n", hex.EncodeToString(result.UserID[:]), result.Username, hex.EncodeToString(result.SiteID[:]), result.SiteName)
	}
	return nil
}
