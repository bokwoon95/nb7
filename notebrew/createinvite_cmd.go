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
	"strconv"
	"strings"
	"time"

	"github.com/bokwoon95/nb7"
	"github.com/bokwoon95/sq"
	"golang.org/x/crypto/blake2b"
)

type CreateinviteCmd struct {
	Notebrew *nb7.Notebrew
	Stdout   io.Writer
	Count    sql.NullInt64
}

func CreateinviteCommand(nbrew *nb7.Notebrew, args ...string) (*CreateinviteCmd, error) {
	var cmd CreateinviteCmd
	cmd.Notebrew = nbrew
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.Func("count", "", func(s string) error {
		count, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("%q is not a valid count", s)
		}
		cmd.Count = sql.NullInt64{Int64: count, Valid: true}
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
	return &cmd, nil
}

func (cmd *CreateinviteCmd) Run() error {
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	count := 1
	if cmd.Count.Valid {
		count = int(cmd.Count.Int64)
	}
	for i := 0; i < count; i++ {
		var signupToken [8 + 16]byte
		binary.BigEndian.PutUint64(signupToken[:8], uint64(time.Now().Unix()))
		_, err := rand.Read(signupToken[8:])
		if err != nil {
			return err
		}
		checksum := blake2b.Sum256(signupToken[8:])
		var signupTokenHash [8 + blake2b.Size256]byte
		copy(signupTokenHash[:8], signupToken[:8])
		copy(signupTokenHash[8:], checksum[:])
		_, err = sq.Exec(cmd.Notebrew.DB, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format:  "INSERT INTO signup (signup_token_hash) VALUES ({signupTokenHash})",
			Values: []any{
				sq.BytesParam("signupTokenHash", signupTokenHash[:]),
			},
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.Stdout, cmd.Notebrew.Scheme+cmd.Notebrew.AdminDomain+"/admin/signup/?token="+strings.TrimLeft(hex.EncodeToString(signupToken[:]), "0"))
	}
	return nil
}
