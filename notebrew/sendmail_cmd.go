package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bokwoon95/nb7"
)

type SendmailCmd struct {
	Notebrew *nb7.Notebrew
	From     string
	To       []string
	Subject  string
	Body     string
}

func SendmailCommand(nbrew *nb7.Notebrew, args ...string) (*SendmailCmd, error) {
	var cmd SendmailCmd
	cmd.Notebrew = nbrew
	fileinfo, err := os.Stdin.Stat()
	if err != nil {
		return nil, err
	}
	if (fileinfo.Mode() & os.ModeCharDevice) == 0 {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		cmd.Body = string(b)
	}
	flagset := flag.NewFlagSet("", flag.ContinueOnError)
	flagset.StringVar(&cmd.From, "from", "", "")
	flagset.Func("to", "", func(s string) error {
		cmd.To = append(cmd.To, s)
		return nil
	})
	flagset.StringVar(&cmd.Subject, "subject", "", "")
	flagset.StringVar(&cmd.Body, "body", "", "")
	err = flagset.Parse(args)
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

func (cmd *SendmailCmd) Run() error {
	localDir, err := filepath.Abs(fmt.Sprint(cmd.Notebrew.FS))
	if err == nil {
		fileInfo, err := os.Stat(localDir)
		if err != nil || !fileInfo.IsDir() {
			localDir = ""
		}
	}
	b, err := fs.ReadFile(cmd.Notebrew.FS, "config/mailer.txt")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("mailer not configured: %s does not exist", filepath.Join(localDir, "config/mailer.txt"))
		}
		return err
	}
	rawURL := string(bytes.TrimSpace(b))
	if strings.HasPrefix(rawURL, "file:") {
		filename := strings.TrimPrefix(strings.TrimPrefix(rawURL, "file:"), "//")
		b, err := os.ReadFile(filename)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%s: %s does not exist", filepath.Join(localDir, "config/mailer.txt"), filename)
			}
			return nil
		}
		rawURL = string(bytes.TrimSpace(b))
	}
	const expectedFormat = "smtp://user@mail.com:password@smtp.server.com:587"
	if rawURL == "" {
		return fmt.Errorf("%s is empty (expected format: %s)", filepath.Join(localDir, "config/mailer.txt"), expectedFormat)
	}
	uri, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s: %q is not a valid URL (expected format: %s)", filepath.Join(localDir, "config/mailer.txt"), rawURL, expectedFormat)
	}
	if uri.Scheme != "smtp" {
		return fmt.Errorf("%s: %q is not an SMTP URL (expected format: %s)", filepath.Join(localDir, "config/mailer.txt"), rawURL, expectedFormat)
	}
	if uri.User == nil {
		return fmt.Errorf("%s: %q is missing the username (expected format: %s)", filepath.Join(localDir, "config/mailer.txt"), rawURL, expectedFormat)
	}
	username := uri.User.Username()
	password, ok := uri.User.Password()
	if !ok {
		return fmt.Errorf("%s: %q is missing the password (expected format: %s)", filepath.Join(localDir, "config/mailer.txt"), rawURL, expectedFormat)
	}
	port := uri.Port()
	if port == "" {
		return fmt.Errorf("%s: %q is missing the port number (expected format: %s)", filepath.Join(localDir, "config/mailer.txt"), rawURL, expectedFormat)
	}
	if len(cmd.To) == 0 {
		return fmt.Errorf("no recipient(s) specified")
	}
	// username password host port
	auth := smtp.PlainAuth("", username, password, strings.TrimSuffix(uri.Host, ":"+port))
	from := username
	if cmd.From != "" {
		from = cmd.From
	}
	from = strings.ReplaceAll(strings.ReplaceAll(from, "\r", ""), "\n", "")
	to := strings.ReplaceAll(strings.ReplaceAll(strings.Join(cmd.To, ", "), "\r", ""), "\n", "")
	subject := strings.ReplaceAll(strings.ReplaceAll(cmd.Subject, "\r", ""), "\n", "")
	body := "MIME-version: 1.0\r\n" +
		"Content-Type: text/html; charset=\"UTF-8\"\r\n" +
		"From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"\r\n" +
		cmd.Body + "\r\n"
	err = smtp.SendMail(uri.Host, auth, cmd.From, cmd.To, []byte(body))
	if err != nil {
		return fmt.Errorf("email sending failed: %v", err)
	}
	return nil
}
