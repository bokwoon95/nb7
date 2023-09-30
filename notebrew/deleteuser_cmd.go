package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/bokwoon95/nb7"
	"github.com/bokwoon95/sq"
)

type DeleteuserCmd struct {
	Notebrew *nb7.Notebrew
	Username string
}

func DeleteuserCommand(nbrew *nb7.Notebrew, args ...string) (*DeleteuserCmd, error) {
	var cmd DeleteuserCmd
	cmd.Notebrew = nbrew
	var confirm bool
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.BoolVar(&confirm, "confirm", false, "")
	flagset.Usage = func() {
		fmt.Fprintln(flagset.Output(), `Usage:
  lorem ipsum dolor sit amet
  consectetur adipiscing elit
Flags:`)
		flagset.PrintDefaults()
	}
	err := flagset.Parse(args)
	if err != nil {
		return nil, err
	}
	args = flagset.Args()
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			err := flagset.Parse(args[i:])
			if err != nil {
				return nil, err
			}
			args = args[:i]
			break
		}
	}
	if len(args) > 1 {
		flagset.Usage()
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(args[1:], " "))
	}
	if len(args) == 1 {
		cmd.Username = args[0]
		if confirm {
			return &cmd, nil
		}
		validationError, err := cmd.validateUsername(cmd.Username)
		if err != nil {
			return nil, err
		}
		if validationError != "" {
			return nil, fmt.Errorf(validationError)
		}
	}
	fmt.Println("Press Ctrl+C to exit.")
	reader := bufio.NewReader(os.Stdin)
	if len(args) == 0 {
		for {
			fmt.Print("Username (leave blank for the default user): ")
			text, err := reader.ReadString('\n')
			if err != nil {
				return nil, err
			}
			cmd.Username = strings.TrimSpace(text)
			validationError, err := cmd.validateUsername(cmd.Username)
			if err != nil {
				return nil, err
			}
			if validationError != "" {
				fmt.Println(validationError)
				continue
			}
			break
		}
	}
	if !confirm {
		for {
			fmt.Printf("Delete user %[1]q? This action is irreversible. All files belonging to %[1]q will be deleted. (y/n): ", cmd.Username)
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

func (cmd *DeleteuserCmd) Run() error {
	validationError, err := cmd.validateUsername(cmd.Username)
	if err != nil {
		return err
	}
	if validationError != "" {
		return fmt.Errorf(validationError)
	}
	if cmd.Username != "" {
		var sitePrefix string
		if strings.Contains(cmd.Username, ".") {
			sitePrefix = cmd.Username
		} else {
			sitePrefix = "@" + cmd.Username
		}
		err = nb7.RemoveAll(cmd.Notebrew.FS, sitePrefix)
		if err != nil {
			return err
		}
	}
	tx, err := cmd.Notebrew.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = sq.Exec(tx, sq.CustomQuery{
		Dialect: cmd.Notebrew.Dialect,
		Format: "DELETE FROM site_user WHERE EXISTS (" +
			"SELECT 1" +
			" FROM site" +
			" WHERE site.site_id = site_user.site_id" +
			" AND site.site_name = {username}" +
			" UNION ALL" +
			" SELECT 1" +
			" FROM users" +
			" WHERE users.user_id = site_user.user_id" +
			" AND users.username = {username}" +
			")",
		Values: []any{
			sq.StringParam("username", cmd.Username),
		},
	})
	_, err = sq.Exec(tx, sq.CustomQuery{
		Dialect: cmd.Notebrew.Dialect,
		Format:  "DELETE FROM users WHERE username = {username}",
		Values: []any{
			sq.StringParam("username", cmd.Username),
		},
	})
	if err != nil {
		return err
	}
	_, err = sq.Exec(tx, sq.CustomQuery{
		Dialect: cmd.Notebrew.Dialect,
		Format:  "DELETE FROM site WHERE site_name = {username}",
		Values: []any{
			sq.StringParam("username", cmd.Username),
		},
	})
	if err != nil {
		return err
	}
	err = tx.Commit()
	if err != nil {
		return err
	}
	return nil
}

func (cmd *DeleteuserCmd) validateUsername(username string) (validationError string, err error) {
	if username != "" {
		var sitePrefix string
		if strings.Contains(username, ".") {
			sitePrefix = username
		} else {
			sitePrefix = "@" + username
		}
		_, err = fs.Stat(cmd.Notebrew.FS, sitePrefix)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "user does not exist", nil
			}
			return "", err
		}
	}
	exists, err := sq.FetchExists(cmd.Notebrew.DB, sq.CustomQuery{
		Dialect: cmd.Notebrew.Dialect,
		Format:  "SELECT 1 FROM users WHERE username = {username}",
		Values: []any{
			sq.StringParam("username", username),
		},
	})
	if err != nil {
		return "", err
	}
	if !exists {
		return "user does not exist", nil
	}
	return "", nil
}
