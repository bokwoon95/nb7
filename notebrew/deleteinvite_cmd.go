package main

import (
	"bufio"
	"database/sql"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bokwoon95/nb7"
	"github.com/bokwoon95/sq"
	"golang.org/x/crypto/blake2b"
)

type DeleteinviteCmd struct {
	Notebrew *nb7.Notebrew
	Before   sql.NullTime
	After    sql.NullTime
}

func DeleteinviteCommand(nbrew *nb7.Notebrew, args ...string) (*DeleteinviteCmd, error) {
	var cmd DeleteinviteCmd
	cmd.Notebrew = nbrew
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.Func("before", "", func(s string) error {
		before, err := parseTime(s)
		if err != nil {
			return err
		}
		cmd.Before = before
		return nil
	})
	flagset.Func("after", "", func(s string) error {
		after, err := parseTime(s)
		if err != nil {
			return err
		}
		cmd.After = after
		return nil
	})
	err := flagset.Parse(args)
	if err != nil {
		return nil, err
	}
	flagArgs := flagset.Args()
	if len(flagArgs) > 0 {
		flagset.Usage()
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(flagArgs, " "))
	}
	if !cmd.Before.Valid && !cmd.After.Valid {
		fmt.Println("Press Ctrl+C to exit.")
		reader := bufio.NewReader(os.Stdin)
		for {
			fmt.Print("Delete all invites? (y/n): ")
			text, err := reader.ReadString('\n')
			if err != nil {
				return nil, err
			}
			text = strings.TrimSpace(text)
			if text == "y" {
				break
			}
			if text == "n" {
				fmt.Println("cancelled")
				return nil, flag.ErrHelp
			}
		}
	}
	return &cmd, nil
}

func (cmd *DeleteinviteCmd) Run() error {
	var conditions []sq.Predicate
	if cmd.Before.Valid {
		tokenHash := make([]byte, 8+blake2b.Size256)
		binary.BigEndian.PutUint64(tokenHash[:8], uint64(cmd.Before.Time.Unix()))
		conditions = append(conditions, sq.Expr("signup_token_hash < {}", tokenHash))
	}
	if cmd.After.Valid {
		tokenHash := make([]byte, 8+blake2b.Size256)
		binary.BigEndian.PutUint64(tokenHash[:8], uint64(cmd.After.Time.Unix()))
		conditions = append(conditions, sq.Expr("signup_token_hash > {}", tokenHash))
	}
	if len(conditions) == 0 {
		conditions = []sq.Predicate{sq.Expr("1 = 1")}
	}
	result, err := sq.Exec(cmd.Notebrew.DB, sq.CustomQuery{
		Dialect: cmd.Notebrew.Dialect,
		Format:  "DELETE FROM signup WHERE {conditions}",
		Values: []any{
			sq.Param("conditions", sq.And(conditions...)),
		},
	})
	if err != nil {
		return err
	}
	if result.RowsAffected == 1 {
		fmt.Println("1 invite deleted")
	} else {
		fmt.Println(strconv.FormatInt(result.RowsAffected, 10) + " invites deleted")
	}
	return nil
}

func parseTime(s string) (sql.NullTime, error) {
	if s == "" {
		return sql.NullTime{}, nil
	}
	if s == "now" {
		return sql.NullTime{Time: time.Now(), Valid: true}, nil
	}
	for _, format := range []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02T15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02T15:04:05.999999999-07",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(format, s, time.UTC); err == nil {
			return sql.NullTime{Time: t, Valid: true}, nil
		}
	}
	return sql.NullTime{}, fmt.Errorf("not a valid time string")
}
