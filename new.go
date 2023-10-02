package nb7

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"
)

func New(fsys FS) (*Notebrew, error) {
	nbrew := &Notebrew{
		FS:        fsys,
		ErrorCode: func(error) string { return "" },
	}
	localDir, err := filepath.Abs(fmt.Sprint(nbrew.FS))
	if err == nil {
		fileInfo, err := os.Stat(localDir)
		if err != nil || !fileInfo.IsDir() {
			localDir = ""
		}
	}

	// Read from config/address.txt.
	b, err := fs.ReadFile(nbrew.FS, "config/address.txt")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %v", filepath.Join(localDir, "config/address.txt"), err)
		}
		nbrew.Scheme = "http://"
		nbrew.AdminDomain = "localhost:6444"
		nbrew.ContentDomain = "localhost:6444"
	} else {
		address := strings.TrimSpace(string(b))
		if address == "" {
			nbrew.Scheme = "http://"
			nbrew.AdminDomain = "localhost:6444"
			nbrew.ContentDomain = "localhost:6444"
		} else {
			lines := strings.Split(address, "\n")
			if len(lines) == 1 {
				nbrew.AdminDomain = strings.TrimSpace(lines[0])
				nbrew.ContentDomain = strings.TrimSpace(lines[0])
			} else if len(lines) == 2 {
				nbrew.AdminDomain = strings.TrimSpace(lines[0])
				nbrew.ContentDomain = strings.TrimSpace(lines[1])
			} else {
				return nil, fmt.Errorf("%s contains too many lines, maximum 2 lines."+
					" The first line is the admin domain, the second line is the content domain."+
					" Alternatively, if only one line is provided it will be used as as both the admin domain and content domain.",
					filepath.Join(localDir, "config/address.txt"),
				)
			}
			if strings.Contains(nbrew.AdminDomain, "127.0.0.1") {
				return nil, fmt.Errorf(
					"%s: %q: don't use 127.0.0.1, use localhost instead",
					filepath.Join(localDir, "config/address.txt"),
					nbrew.AdminDomain,
				)
			}
			if strings.Contains(nbrew.ContentDomain, "127.0.0.1") {
				return nil, fmt.Errorf(
					"%s: %q: don't use 127.0.0.1, use localhost instead",
					filepath.Join(localDir, "config/address.txt"),
					nbrew.ContentDomain,
				)
			}
			localhostAdmin := nbrew.AdminDomain == "localhost" || strings.HasPrefix(nbrew.AdminDomain, "localhost:")
			localhostContent := nbrew.ContentDomain == "localhost" || strings.HasPrefix(nbrew.ContentDomain, "localhost:")
			if localhostAdmin && localhostContent {
				nbrew.Scheme = "http://"
				if nbrew.AdminDomain != nbrew.ContentDomain {
					return nil, fmt.Errorf(
						"%s: %q, %q: if localhost, addresses must be the same",
						filepath.Join(localDir, "config/address.txt"),
						nbrew.AdminDomain,
						nbrew.ContentDomain,
					)
				}
				if strings.HasPrefix(nbrew.AdminDomain, "localhost:") {
					_, err = strconv.Atoi(strings.TrimPrefix(nbrew.AdminDomain, "localhost:"))
					if err != nil {
						return nil, fmt.Errorf(
							"%s: %q: localhost port invalid, must be a number e.g. localhost:6444",
							filepath.Join(localDir, "config/address.txt"),
							nbrew.AdminDomain,
						)
					}
				}
				if strings.HasPrefix(nbrew.ContentDomain, "localhost:") {
					_, err = strconv.Atoi(strings.TrimPrefix(nbrew.ContentDomain, "localhost:"))
					if err != nil {
						return nil, fmt.Errorf(
							"%s: %q: localhost port invalid, must be a number e.g. localhost:6444",
							filepath.Join(localDir, "config/address.txt"),
							nbrew.ContentDomain,
						)
					}
				}
			} else if !localhostAdmin && !localhostContent {
				nbrew.Scheme = "https://"
				if !strings.Contains(nbrew.AdminDomain, ".") {
					return nil, fmt.Errorf("%s: %q is not a valid domain (e.g. example.com):"+
						" missing a top level domain (.com, .org, .net, etc)",
						filepath.Join(localDir, "config/address.txt"),
						nbrew.AdminDomain,
					)
				}
				for _, char := range nbrew.AdminDomain {
					if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || char == '.' || char == '-' {
						continue
					}
					return nil, fmt.Errorf("%s: %q is not a valid domain:"+
						" only lowercase letters, numbers, dot and hyphen are allowed e.g. example.com",
						filepath.Join(localDir, "config/address.txt"),
						nbrew.AdminDomain,
					)
				}
				if !strings.Contains(nbrew.ContentDomain, ".") {
					return nil, fmt.Errorf("%s: %q is not a valid domain:"+
						" missing a top level domain (.com, .org, .net, etc)",
						filepath.Join(localDir, "config/address.txt"),
						nbrew.ContentDomain,
					)
				}
				for _, char := range nbrew.ContentDomain {
					if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || char == '.' || char == '-' {
						continue
					}
					return nil, fmt.Errorf("%s: %q is not a valid domain (e.g. example.com):"+
						" only lowercase letters, numbers, dot and hyphen are allowed e.g. example.com",
						filepath.Join(localDir, "config/address.txt"),
						nbrew.ContentDomain,
					)
				}
			} else {
				return nil, fmt.Errorf(
					"%s: %q, %q: localhost and non-localhost addresses cannot be mixed",
					filepath.Join(localDir, "config/address.txt"),
					nbrew.AdminDomain,
					nbrew.ContentDomain,
				)
			}
		}
	}

	// Read from config/multisite.txt.
	b, err = fs.ReadFile(nbrew.FS, "config/multisite.txt")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %v", filepath.Join(localDir, "config/multisite.txt"), err)
		}
	} else {
		nbrew.MultisiteMode = strings.ToLower(string(b))
	}
	if nbrew.MultisiteMode != "" && nbrew.MultisiteMode != "subdomain" && nbrew.MultisiteMode != "subdirectory" {
		return nil, fmt.Errorf(
			`%s: %q is not a valid multisite value (accepted values: "", "subdomain", "subdirectory")`,
			filepath.Join(localDir, "config/multisite.txt"),
			nbrew.MultisiteMode,
		)
	}

	// Read from config/database.txt.
	var dsn string
	b, err = fs.ReadFile(nbrew.FS, "config/database.txt")
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if nbrew.Scheme == "https://" {
			// If database.txt doesn't exist but we are serving a live site, we
			// have to create a database. In this case, fall back to an SQLite
			// database.
			dsn = "sqlite"
		}
	} else {
		dsn = strings.TrimSpace(string(b))
		if strings.HasPrefix(dsn, "file:") {
			filename := strings.TrimPrefix(strings.TrimPrefix(dsn, "file:"), "//")
			file, err := os.Open(filename)
			if err != nil {
				ext := filepath.Ext(filename)
				if errors.Is(err, fs.ErrNotExist) && (ext == ".sqlite" || ext == ".sqlite3" || ext == ".db" || ext == ".db3") {
					dsn = filename
				} else {
					return nil, fmt.Errorf("%s: opening %q: %v", filepath.Join(localDir, "config/database.txt"), dsn, err)
				}
			} else {
				defer file.Close()
				r := bufio.NewReader(file)
				// SQLite databases may also start with a 'file:' prefix. Treat
				// the contents of the file as a dsn only if the file isn't
				// already an SQLite database i.e. the first 16 bytes isn't the
				// SQLite file header.
				// https://www.sqlite.org/fileformat.html#the_database_header
				header, err := r.Peek(16)
				if err != nil {
					return nil, fmt.Errorf("%s: reading %q: %v", filepath.Join(localDir, "config/database.txt"), dsn, err)
				}
				if string(header) == "SQLite format 3\x00" {
					dsn = "sqlite:" + filename
				} else {
					var b strings.Builder
					_, err = r.WriteTo(&b)
					if err != nil {
						return nil, fmt.Errorf("%s: reading %q: %v", filepath.Join(localDir, "config/database.txt"), dsn, err)
					}
					dsn = strings.TrimSpace(b.String())
				}
			}
		}
	}
	if dsn != "" {
		// Determine the database dialect from the dsn.
		if dsn == "sqlite" {
			nbrew.Dialect = "sqlite"
			if localDir == "" {
				return nil, fmt.Errorf("unable to create sqlite database")
			}
			dsn = filepath.Join(localDir, "notebrew.db")
		} else if strings.HasPrefix(dsn, "sqlite:") || strings.HasPrefix(dsn, "sqlite3:") {
			nbrew.Dialect = "sqlite"
		} else if strings.HasPrefix(dsn, "postgres://") {
			nbrew.Dialect = "postgres"
		} else if strings.HasPrefix(dsn, "mysql://") {
			nbrew.Dialect = "mysql"
		} else if strings.HasPrefix(dsn, "sqlserver://") {
			nbrew.Dialect = "sqlserver"
		} else if strings.Contains(dsn, "@tcp(") || strings.Contains(dsn, "@unix(") {
			nbrew.Dialect = "mysql"
		} else {
			ext := filepath.Ext(dsn)
			if ext == ".sqlite" || ext == ".sqlite3" || ext == ".db" || ext == ".db3" {
				nbrew.Dialect = "sqlite"
			} else {
				return nil, fmt.Errorf("%s: unknown or unsupported dataSourceName %q", filepath.Join(localDir, "config/database.txt"), dsn)
			}
		}
		// Set a default driverName depending on the dialect.
		var driverName string
		switch nbrew.Dialect {
		case "sqlite":
			// Assumes driver will be github.com/mattn/go-sqlite3.
			driverName = "sqlite3"
		case "postgres":
			// Assumes driver will be github.com/lib/pq.
			driverName = "postgres"
		case "mysql":
			// Assumes driver will be github.com/go-sql-driver/mysql.
			driverName = "mysql"
		case "sqlserver":
			// Assumes driver will be github.com/denisenkom/go-mssqldb.
			driverName = "sqlserver"
		}
		// Check if the user registered any driverName/dsn overrides
		// for the dialect.
		dbDriversMu.RLock()
		d := dbDrivers[nbrew.Dialect]
		dbDriversMu.RUnlock()
		if d.DriverName != "" {
			driverName = d.DriverName
		}
		if d.PreprocessDSN != nil {
			dsn, err = d.PreprocessDSN(dsn)
			if err != nil {
				return nil, err
			}
		} else {
			// Do some default dsn cleaning if no custom dialect Driver was
			// registered. We assume the default drivers of
			// "github.com/mattn/go-sqlite3" and
			// "github.com/go-sql-driver/mysql", which don't accept "sqlite:"
			// or "mysql://" prefixes so trim that away.
			switch nbrew.Dialect {
			case "sqlite":
				for _, prefix := range []string{"sqlite3://", "sqlite3:", "sqlite://", "sqlite:"} {
					if strings.HasPrefix(dsn, prefix) {
						dsn = strings.TrimPrefix(dsn, prefix)
						break
					}
				}
			case "mysql":
				dsn = strings.TrimPrefix(dsn, "mysql://")
			}
		}
		if d.ErrorCode != nil {
			nbrew.ErrorCode = d.ErrorCode
		}
		// Open the database using the driverName and dsn.
		nbrew.DB, err = sql.Open(driverName, dsn)
		if err != nil {
			return nil, fmt.Errorf(
				"%s: opening database with driverName %q and dsn %q: %w",
				filepath.Join(localDir, "config/database.txt"),
				driverName,
				dsn,
				err,
			)
		}
		err = automigrate(nbrew.Dialect, nbrew.DB)
		if err != nil {
			return nil, fmt.Errorf("%s: automigrate failed: %w", filepath.Join(localDir, "config/database.txt"), err)
		}
	}

	dirs := []string{
		"notes",
		"output",
		"output/images",
		"output/themes",
		"pages",
		"posts",
		"system",
	}
	for _, dir := range dirs {
		err = nbrew.FS.Mkdir(dir, 0755)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			log.Println(err)
		}
	}
	return nbrew, nil
}

var (
	readTimeout  time.Duration = 5 * time.Second
	writeTimeout time.Duration = 5 * time.Second
	idleTimeout  time.Duration = 120 * time.Second
)

func (nbrew *Notebrew) NewServer() (*http.Server, error) {
	server := &http.Server{
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
		Addr:         nbrew.AdminDomain,
		Handler:      nbrew,
	}
	if nbrew.Scheme == "https://" {
		server.Addr = ":443"
		certConfig := certmagic.NewDefault()
		domainNames := []string{nbrew.AdminDomain}
		if nbrew.ContentDomain != "" && nbrew.ContentDomain != nbrew.AdminDomain {
			domainNames = append(domainNames, nbrew.ContentDomain)
		}
		if nbrew.MultisiteMode == "subdomain" {
			if certmagic.DefaultACME.DNS01Solver == nil && certmagic.DefaultACME.CA == certmagic.LetsEncryptProductionCA {
				dir, err := filepath.Abs(fmt.Sprint(nbrew.FS))
				if err == nil {
					fileInfo, err := os.Stat(dir)
					if err != nil || !fileInfo.IsDir() {
						dir = ""
					}
				}
				return nil, fmt.Errorf(`%s: "subdomain" not supported, use "subdirectory" instead (more info: https://notebrew.com/path/to/docs/)`, filepath.Join(dir, "config/multisite.txt"))
			}
			domainNames = append(domainNames, "*."+nbrew.ContentDomain)
		}
		err := certConfig.ManageAsync(context.Background(), domainNames)
		if err != nil {
			return nil, err
		}
		server.TLSConfig = certConfig.TLSConfig()
		server.TLSConfig.NextProtos = []string{"h2", "http/1.1", "acme-tls/1"}
	}
	return server, nil
}

func (nbrew *Notebrew) Close() error {
	if nbrew.DB == nil {
		return nil
	}
	if nbrew.Dialect == "sqlite" {
		nbrew.DB.Exec("PRAGMA analysis_limit(400); PRAGMA optimize;")
	}
	return nbrew.DB.Close()
}

var (
	dbDriversMu sync.RWMutex
	dbDrivers   = make(map[string]Driver)
)

// Driver represents the capabilities of the underlying database driver for a
// particular dialect. It is not necessary to implement all fields.
type Driver struct {
	// (Required) Dialect is the database dialect. Possible values: "sqlite", "postgres",
	// "mysql".
	Dialect string

	// (Required) DriverName is the driverName to be used with sql.Open().
	DriverName string

	// ErrorCode translates a database error into an dialect-specific error
	// code. If the error is not a database error or no error code can be
	// determined, ErrorCode should return an empty string.
	ErrorCode func(error) string

	// If not nil, PreprocessDSN will be called on a dataSourceName right
	// before it is passed in to sql.Open().
	PreprocessDSN func(string) (string, error)
}

// Registers registers a driver for a particular database dialect.
func RegisterDriver(d Driver) {
	dbDriversMu.Lock()
	defer dbDriversMu.Unlock()
	if d.Dialect == "" {
		panic("notebrew: driver dialect cannot be empty")
	}
	if _, dup := dbDrivers[d.Dialect]; dup {
		panic("notebrew: RegisterDialect called twice for dialect " + d.Dialect)
	}
	dbDrivers[d.Dialect] = d
}
