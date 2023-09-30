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

	"golang.org/x/crypto/blake2b"
)

type TokenCmd struct {
	Stdout io.Writer
	Input  sql.NullString
}

func TokenCommand(args ...string) (*TokenCmd, error) {
	var cmd TokenCmd
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

func (cmd *TokenCmd) Run() error {
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if !cmd.Input.Valid {
		var token [8 + 16]byte
		binary.BigEndian.PutUint64(token[:8], uint64(time.Now().Unix()))
		_, err := rand.Read(token[8:])
		if err != nil {
			return err
		}
		checksum := blake2b.Sum256(token[8:])
		var tokenHash [8 + blake2b.Size256]byte
		copy(tokenHash[:8], token[:8])
		copy(tokenHash[8:], checksum[:])
		fmt.Fprintln(cmd.Stdout, hex.EncodeToString(token[:])+"\n"+hex.EncodeToString(tokenHash[:]))
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
			var token [8 + 16]byte
			binary.BigEndian.PutUint64(token[:8], uint64(t.Unix()))
			_, err := rand.Read(token[8:])
			if err != nil {
				return err
			}
			checksum := blake2b.Sum256(token[8:])
			var tokenHash [8 + blake2b.Size256]byte
			copy(tokenHash[:8], token[:8])
			copy(tokenHash[8:], checksum[:])
			fmt.Fprintln(cmd.Stdout, hex.EncodeToString(token[:])+"\n"+hex.EncodeToString(tokenHash[:]))
			return nil
		}
	}
	// Example:
	// 00000000651076f810d48bc8dc3f6102c0b266ca4d951dc0
	// 00000000651076f86d0f2f47d3d015460f71b3b3a552eade7eebc4372b436907ca0551f9f95acde9
	if len(cmd.Input.String) > 80 {
		return fmt.Errorf("input is not a valid timestamp, token or token hash")
	}
	if len(cmd.Input.String) > 48 {
		cmd.Input.String = fmt.Sprintf("%080s", cmd.Input.String)
	} else {
		cmd.Input.String = fmt.Sprintf("%048s", cmd.Input.String)
	}
	b, err := hex.DecodeString(cmd.Input.String)
	if err != nil {
		return fmt.Errorf("input is not a valid timestamp, token or token hash")
	}
	var timestamp [8]byte
	copy(timestamp[:], b[:8])
	fmt.Fprintln(cmd.Stdout, time.Unix(int64(binary.BigEndian.Uint64(timestamp[:])), 0).UTC().Format("2006-01-02 15:04:05Z"))
	if len(b) == 24 {
		checksum := blake2b.Sum256(b[8:])
		var tokenHash [8 + blake2b.Size256]byte
		copy(tokenHash[:8], b[:8])
		copy(tokenHash[8:], checksum[:])
		fmt.Fprintln(cmd.Stdout, hex.EncodeToString(tokenHash[:]))
	}
	return nil
}
