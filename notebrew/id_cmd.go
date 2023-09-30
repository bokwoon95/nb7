package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bokwoon95/nb7"
	"github.com/google/uuid"
)

type IdCmd struct {
	Stdout io.Writer
	Input  sql.NullString
}

func IdCommand(args ...string) (*IdCmd, error) {
	var cmd IdCmd
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.Usage = func() {
		fmt.Fprintln(flagset.Output(), `Usage:
  lorem ipsum dolor sit amet
  consectetur adipiscing elit`)
	}
	err := flagset.Parse(args)
	if err != nil {
		return nil, err
	}
	args = flagset.Args()
	if len(args) > 1 {
		flagset.Usage()
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(args[1:], " "))
	}
	if len(args) == 1 {
		cmd.Input = sql.NullString{String: args[0], Valid: true}
	}
	return &cmd, nil
}

func (cmd *IdCmd) Run() error {
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if !cmd.Input.Valid {
		id := nb7.NewID()
		fmt.Fprintln(cmd.Stdout, hex.EncodeToString(id[:]))
		return nil
	}
	cmd.Input.String = strings.TrimSuffix(cmd.Input.String, "Z")
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
		if t, err := time.ParseInLocation(format, cmd.Input.String, time.UTC); err == nil {
			var timestamp [8]byte
			binary.BigEndian.PutUint64(timestamp[:], uint64(t.Unix()))
			var id [16]byte
			copy(id[:5], timestamp[len(timestamp)-5:])
			_, err := rand.Read(id[5:])
			if err != nil {
				panic(err)
			}
			fmt.Fprintln(cmd.Stdout, hex.EncodeToString(id[:]))
			return nil
		}
	}
	id, err := uuid.Parse(cmd.Input.String)
	if err != nil {
		return fmt.Errorf("input is not a valid timestamp or id")
	}
	var timestamp [8]byte
	copy(timestamp[len(timestamp)-5:], id[:5])
	fmt.Fprintln(cmd.Stdout, time.Unix(int64(binary.BigEndian.Uint64(timestamp[:])), 0).UTC().Format("2006-01-02 15:04:05Z"))
	return nil
}
