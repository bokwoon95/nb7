package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bokwoon95/nb7"
	"github.com/bokwoon95/sq"
)

var open = func(address string) {}

func main() {
	err := func() error {
		var dir, address, multisite, database, showQueries string
		flagset := flag.NewFlagSet("", flag.ContinueOnError)
		flagset.StringVar(&dir, "dir", "", "")
		flagset.StringVar(&address, "address", "", "")
		flagset.StringVar(&multisite, "multisite", "", "")
		flagset.StringVar(&database, "database", "", "")
		flagset.StringVar(&showQueries, "show-queries", "", "")
		err := flagset.Parse(os.Args[1:])
		if err != nil {
			return err
		}

		dir = strings.TrimSpace(dir)
		if dir == "" {
			userHomeDir, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			dir = filepath.Join(userHomeDir, "notebrew-admin")
		}
		err = os.MkdirAll(dir, 0755)
		if err != nil {
			return err
		}
		err = os.MkdirAll(path.Join(dir, "config"), 0755)
		if err != nil {
			return err
		}

		address = strings.TrimSpace(address)
		if address != "" {
			if strings.Count(address, ",") > 1 {
				return fmt.Errorf("-addr %q: too many commas (max 1)", address)
			}
			err = os.WriteFile(filepath.Join(dir, "config/address.txt"), []byte(strings.ReplaceAll(address, ",", "\n")), 0644)
			if err != nil {
				return err
			}
		}

		multisite = strings.TrimSpace(multisite)
		if multisite != "" {
			err = os.WriteFile(filepath.Join(dir, "config/multisite.txt"), []byte(multisite), 0644)
			if err != nil {
				return err
			}
		}

		database = strings.TrimSpace(database)
		if database != "" {
			err = os.WriteFile(filepath.Join(dir, "config/database.txt"), []byte(database), 0644)
			if err != nil {
				return err
			}
		}

		args := flagset.Args()
		if len(args) > 0 {
			command, args := args[0], args[1:]
			switch command {
			case "createinvite", "deleteinvite",
				"createsite", "deletesite",
				"createuser", "deleteuser",
				"permissions", "resetpassword":
				// For commands that require a database, configure the database to
				// sqlite if it hasn't already been configured.
				b, err := os.ReadFile(filepath.Join(dir, "config/database.txt"))
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				if len(bytes.TrimSpace(b)) == 0 {
					err = os.WriteFile(filepath.Join(dir, "config/database.txt"), []byte("sqlite"), 0644)
					if err != nil {
						return err
					}
				}
			}
			nbrew, err := NewNotebrew(dir)
			if err != nil {
				return err
			}
			defer nbrew.Close()
			switch command {
			case "createinvite":
				cmd, err := CreateinviteCommand(nbrew, args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "deleteinvite":
				cmd, err := DeleteinviteCommand(nbrew, args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "createsite":
				cmd, err := CreatesiteCommand(nbrew, args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "deletesite":
				cmd, err := DeletesiteCommand(nbrew, args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "createuser":
				cmd, err := CreateuserCommand(nbrew, args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "deleteuser":
				cmd, err := DeleteuserCommand(nbrew, args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "permissions":
				cmd, err := PermissionsCommand(nbrew, args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "resetpassword":
				cmd, err := ResetpasswordCommand(nbrew, args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "sendmail":
				cmd, err := SendmailCommand(nbrew, args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "hashpassword":
				cmd, err := HashpasswordCommand(args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "id":
				cmd, err := IdCommand(args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			case "token":
				cmd, err := TokenCommand(args...)
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("%s: %w", command, err)
				}
			default:
				return fmt.Errorf("unknown command %s", command)
			}
			return nil
		}

		nbrew, err := NewNotebrew(dir)
		if err != nil {
			return err
		}
		defer nbrew.Close()
		server, err := nbrew.NewServer()
		if err != nil {
			return err
		}
		wait := make(chan os.Signal, 1)
		signal.Notify(wait, syscall.SIGINT, syscall.SIGTERM)
		// Don't use ListenAndServe, manually acquire a listener. That way we
		// can report back to the user if the port is already in user.
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			var errno syscall.Errno
			if !errors.As(err, &errno) {
				return err
			}
			// WSAEADDRINUSE copied from
			// https://cs.opensource.google/go/x/sys/+/refs/tags/v0.6.0:windows/zerrors_windows.go;l=2680
			// To avoid importing an entire 3rd party library just to use a constant.
			const WSAEADDRINUSE = syscall.Errno(10048)
			if errno == syscall.EADDRINUSE || runtime.GOOS == "windows" && errno == WSAEADDRINUSE {
				if nbrew.Scheme == "https://" {
					fmt.Println(server.Addr + " already in use")
					return nil
				}
				open("http://" + server.Addr + "/admin/")
				fmt.Println("http://" + server.Addr)
				return nil
			} else {
				return err
			}
		}
		if nbrew.Scheme == "https://" {
			go http.ListenAndServe(":80", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" && r.Method != "HEAD" {
					http.Error(w, "Use HTTPS", http.StatusBadRequest)
					return
				}
				host, _, err := net.SplitHostPort(r.Host)
				if err != nil {
					host = r.Host
				} else {
					host = net.JoinHostPort(host, "443")
				}
				http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusFound)
			}))
			fmt.Println("Listening on " + server.Addr)
			go server.ServeTLS(listener, "", "")
		} else {
			open("http://" + server.Addr + "/admin/")
			// NOTE: We may need to give a more intricate ASCII header in order for the
			// GUI double clickers to realize that the terminal window is important, so
			// that they won't accidentally close it thinking it is some random
			// terminal.
			fmt.Println("Listening on http://" + server.Addr)
			go server.Serve(listener)
		}
		<-wait
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		server.Shutdown(ctx)
		return nil
	}()
	if err != nil && !errors.Is(err, flag.ErrHelp) && !errors.Is(err, io.EOF) {
		fmt.Println(err)
		pressAnyKeyToExit()
		os.Exit(1)
	}
}

func NewNotebrew(dir string) (*nb7.Notebrew, error) {
	nbrew, err := nb7.New(&nb7.LocalFS{RootDir: dir})
	if err != nil {
		return nil, err
	}
	sq.SetDefaultLogSettings(func(ctx context.Context, logSettings *sq.LogSettings) {
		logSettings.IncludeTime = true
		logSettings.IncludeCaller = true
	})
	sq.SetDefaultLogQuery(func(ctx context.Context, queryStats sq.QueryStats) {
		var output struct {
			Status       string   `json:"status"`
			Time         string   `json:"time"`
			Query        string   `json:"query"`
			Args         []string `json:"args,omitempty"`
			RowCount     *int64   `json:"rowCount,omitempty"`
			RowsAffected *int64   `json:"rowsAffected,omitempty"`
			Exists       *bool    `json:"exists,omitempty"`
			Duration     string   `json:"duration"`
			Source       struct {
				Function string `json:"function,omitempty"`
				File     string `json:"file,omitempty"`
				Line     int    `json:"line,omitempty"`
			} `json:"source"`
		}
		output.Status = "OK"
		output.Time = queryStats.StartedAt.Format("2006-01-02T15:04:05.999999999-07:00")
		output.Query = queryStats.Query
		output.Duration = queryStats.TimeTaken.String()
		queryFailed := queryStats.Err != nil && !nbrew.IsKeyViolation(queryStats.Err) && !nbrew.IsForeignKeyViolation(queryStats.Err)
		if queryFailed {
			output.Status = "query failed: " + queryStats.Err.Error()
		}
		if queryStats.RowCount.Valid {
			output.RowCount = &queryStats.RowCount.Int64
		}
		if queryStats.RowsAffected.Valid {
			output.RowsAffected = &queryStats.RowsAffected.Int64
		}
		if queryStats.Exists.Valid {
			output.Exists = &queryStats.Exists.Bool
		}
		if queryStats.CallerFile != "" && queryStats.CallerLine != 0 {
			output.Source.Function = queryStats.CallerFunction
			output.Source.File = queryStats.CallerFile
			output.Source.Line = queryStats.CallerLine
		}

		// If there was an error, always log the query unconditionally.
		if queryFailed {
			output.Args = make([]string, len(queryStats.Args))
			for i, arg := range queryStats.Args {
				output.Args[i] = fmt.Sprintf("%#v", arg)
			}
			var buf bytes.Buffer
			encoder := json.NewEncoder(&buf)
			encoder.SetEscapeHTML(false)
			encoder.Encode(&output)
			os.Stderr.Write(buf.Bytes())
			return
		}

		// Otherwise, log the query depending on the contents inside config/debug.txt.
		file, err := nbrew.FS.Open("config/show-queries.txt")
		if err != nil {
			return
		}
		defer file.Close()
		reader := bufio.NewReader(file)
		b, _ := reader.Peek(6)
		if len(b) == 0 {
			return
		}
		debug, _ := strconv.ParseBool(string(bytes.TrimSpace(b)))
		if debug {
			query, err := sq.Sprintf(queryStats.Dialect, queryStats.Query, queryStats.Args)
			if err != nil {
				output.Args = make([]string, len(queryStats.Args))
				for i, arg := range queryStats.Args {
					output.Args[i] = fmt.Sprintf("%#v", arg)
				}
			} else {
				output.Query = query
				output.Args = nil
			}
			var buf bytes.Buffer
			encoder := json.NewEncoder(&buf)
			encoder.SetEscapeHTML(false)
			encoder.Encode(&output)
			os.Stderr.Write(buf.Bytes())
		}
	})
	sq.DefaultDialect.Store(&nbrew.Dialect)
	return nbrew, nil
}
