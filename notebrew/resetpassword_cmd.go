package main

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/bokwoon95/nb7"
	"github.com/bokwoon95/sq"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/term"
)

type ResetpasswordCmd struct {
	Notebrew     *nb7.Notebrew
	Stdout       io.Writer
	Username     string
	PasswordHash string
	ResetLink    bool
}

func ResetpasswordCommand(nbrew *nb7.Notebrew, args ...string) (*ResetpasswordCmd, error) {
	var cmd ResetpasswordCmd
	cmd.Notebrew = nbrew
	var username sql.NullString
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.Func("username", "", func(s string) error {
		username = sql.NullString{String: s, Valid: true}
		return nil
	})
	flagset.StringVar(&cmd.PasswordHash, "password-hash", "", "")
	flagset.BoolVar(&cmd.ResetLink, "reset-link", false, "")
	err := flagset.Parse(args)
	if err != nil {
		return nil, err
	}
	flagArgs := flagset.Args()
	if len(flagArgs) > 0 {
		flagset.Usage()
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(flagArgs, " "))
	}
	fmt.Println("Press Ctrl+C to exit.")
	reader := bufio.NewReader(os.Stdin)

	if !username.Valid {
		for {
			fmt.Print("Username or Email (leave blank for default user): ")
			text, err := reader.ReadString('\n')
			if err != nil {
				return nil, err
			}
			username.String = strings.TrimSpace(text)
			if !strings.HasPrefix(username.String, "@") && strings.Contains(username.String, "@") {
				email := username.String
				exists, err := sq.FetchExists(cmd.Notebrew.DB, sq.CustomQuery{
					Dialect: cmd.Notebrew.Dialect,
					Format:  "SELECT 1 FROM users WHERE email = {email}",
					Values: []any{
						sq.StringParam("email", email),
					},
				})
				if err != nil {
					return nil, err
				}
				if !exists {
					fmt.Printf("no such user with email %q\n", email)
					continue
				}
			} else {
				username := strings.TrimPrefix(username.String, "@")
				exists, err := sq.FetchExists(cmd.Notebrew.DB, sq.CustomQuery{
					Dialect: cmd.Notebrew.Dialect,
					Format:  "SELECT 1 FROM users WHERE username = {username}",
					Values: []any{
						sq.StringParam("username", username),
					},
				})
				if err != nil {
					return nil, err
				}
				if !exists {
					fmt.Printf("no such user with username %s\n", username)
					continue
				}
			}
			break
		}
	}
	cmd.Username = username.String

	if cmd.ResetLink {
		return &cmd, nil
	}

	if cmd.PasswordHash == "" {
		for {
			fmt.Print("Password (will be hidden from view, leave blank to generate password reset link): ")
			password, err := term.ReadPassword(int(syscall.Stdin))
			fmt.Println()
			if err != nil {
				return nil, err
			}
			if len(password) == 0 {
				cmd.ResetLink = true
				return &cmd, nil
			}
			if utf8.RuneCount(password) < 8 {
				fmt.Println("Password must be at least 8 characters.")
				continue
			}
			if nb7.IsCommonPassword(password) {
				fmt.Println("Password is too common.")
				continue
			}
			fmt.Print("Confirm password (will be hidden from view): ")
			confirmPassword, err := term.ReadPassword(int(syscall.Stdin))
			fmt.Println()
			if err != nil {
				return nil, err
			}
			if subtle.ConstantTimeCompare(password, confirmPassword) != 1 {
				fmt.Fprintln(os.Stderr, "Passwords do not match.")
				continue
			}
			b, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
			if err != nil {
				return nil, err
			}
			cmd.PasswordHash = string(b)
			break
		}
	}
	return &cmd, nil
}

func (cmd *ResetpasswordCmd) Run() error {
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	name := cmd.Username
	if name == "" {
		name = "default user"
	}
	if cmd.ResetLink {
		var resetToken [8 + 16]byte
		binary.BigEndian.PutUint64(resetToken[:8], uint64(time.Now().Unix()))
		_, err := rand.Read(resetToken[8:])
		if err != nil {
			return err
		}
		checksum := blake2b.Sum256(resetToken[8:])
		var resetTokenHash [8 + blake2b.Size256]byte
		copy(resetTokenHash[:8], resetToken[:8])
		copy(resetTokenHash[8:], checksum[:])
		if !strings.HasPrefix(cmd.Username, "@") && strings.Contains(cmd.Username, "@") {
			email := cmd.Username
			_, err = sq.Exec(cmd.Notebrew.DB, sq.CustomQuery{
				Dialect: cmd.Notebrew.Dialect,
				Format:  "UPDATE users SET password_hash = NULL, reset_token_hash = {resetTokenHash} WHERE email = {email}",
				Values: []any{
					sq.BytesParam("resetTokenHash", resetTokenHash[:]),
					sq.StringParam("email", email),
				},
			})
			if err != nil {
				return err
			}
		} else {
			_, err = sq.Exec(cmd.Notebrew.DB, sq.CustomQuery{
				Dialect: cmd.Notebrew.Dialect,
				Format:  "UPDATE users SET password_hash = NULL, reset_token_hash = {resetTokenHash} WHERE username = {username}",
				Values: []any{
					sq.BytesParam("resetTokenHash", resetTokenHash[:]),
					sq.StringParam("username", strings.TrimPrefix(cmd.Username, "@")),
				},
			})
			if err != nil {
				return err
			}
		}
		values := make(url.Values)
		values.Set("token", strings.TrimLeft(hex.EncodeToString(resetToken[:]), "0"))
		fmt.Fprintf(os.Stderr, "Password reset link generated for %s:\n", name)
		_, err = fmt.Fprintln(cmd.Stdout, cmd.Notebrew.Scheme + cmd.Notebrew.AdminDomain + "/admin/resetpassword/?" + values.Encode())
		return err
	}
	tx, err := cmd.Notebrew.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !strings.HasPrefix(cmd.Username, "@") && strings.Contains(cmd.Username, "@") {
		email := cmd.Username
		_, err = sq.Exec(tx, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format: "DELETE FROM authentication" +
				" WHERE EXISTS (SELECT 1" +
				" FROM users" +
				" WHERE users.user_id = authentication.user_id" +
				" AND users.email = {email}" +
				")",
			Values: []any{
				sq.StringParam("email", email),
			},
		})
		if err != nil {
			return err
		}
		_, err = sq.Exec(tx, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format:  "UPDATE users SET password_hash = {passwordHash} WHERE email = {email}",
			Values: []any{
				sq.StringParam("passwordHash", cmd.PasswordHash),
				sq.StringParam("email", email),
			},
		})
		if err != nil {
			return err
		}
	} else {
		username := strings.TrimPrefix(cmd.Username, "@")
		_, err = sq.Exec(tx, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format: "DELETE FROM authentication" +
				" WHERE EXISTS (SELECT 1" +
				" FROM users" +
				" WHERE users.user_id = authentication.user_id" +
				" AND users.username = {username}" +
				")",
			Values: []any{
				sq.StringParam("username", username),
			},
		})
		if err != nil {
			return err
		}
		_, err = sq.Exec(tx, sq.CustomQuery{
			Dialect: cmd.Notebrew.Dialect,
			Format:  "UPDATE users SET password_hash = {passwordHash} WHERE username = {username}",
			Values: []any{
				sq.StringParam("passwordHash", cmd.PasswordHash),
				sq.StringParam("username", username),
			},
		})
		if err != nil {
			return err
		}
	}
	err = tx.Commit()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "password reset for %s\n", name)
	return nil
}
