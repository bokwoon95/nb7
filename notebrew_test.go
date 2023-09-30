package nb7

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/bokwoon95/sq"
	"github.com/bokwoon95/sqddl/ddl"
	"github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
	"modernc.org/sqlite"
)

var (
	sqliteDSN      = flag.String("sqlite", "", "")
	postgresDSN    = flag.String("postgres", "", "")
	mysqlDSN       = flag.String("mysql", "", "")
	sqlserverDSN   = flag.String("sqlserver", "", "")
	databases      = make(map[string]*sql.DB)
	errorCodeFuncs = make(map[string]func(err error) (errorCode string))
)

func TestMain(m *testing.M) {
	flag.Parse()
	if *sqliteDSN == "" {
		*sqliteDSN = "notebrew_test.db"
	}
	before, after, _ := strings.Cut(*sqliteDSN, "?")
	q, err := url.ParseQuery(after)
	if err != nil {
		log.Fatal(err)
	}
	hasPragma := make(map[string]bool)
	values := q["_pragma"]
	for _, value := range values {
		pragma, _, _ := strings.Cut(value, "(")
		hasPragma[pragma] = true
	}
	if !hasPragma["busy_timeout"] {
		q.Add("_pragma", "busy_timeout(10000)") // milliseconds
	}
	if !hasPragma["foreign_keys"] {
		q.Add("_pragma", "foreign_keys(ON)")
	}
	if !hasPragma["journal_mode"] {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	if !hasPragma["synchronous"] {
		q.Add("_pragma", "synchronous(NORMAL)")
	}
	if !q.Has("_txlock") {
		q.Set("_txlock", "immediate")
	}
	query := strings.NewReplacer("%28", "(", "%29", ")").Replace(q.Encode())
	*sqliteDSN = before + "?" + query
	sqliteDB, err := sql.Open("sqlite", *sqliteDSN)
	if err != nil {
		log.Fatal(err)
	}
	wipeCmd := ddl.WipeCmd{
		DB:      sqliteDB,
		Dialect: ddl.DialectSQLite,
	}
	err = wipeCmd.Run()
	if err != nil {
		log.Fatal(err)
	}
	err = automigrate(ddl.DialectSQLite, sqliteDB)
	if err != nil {
		log.Fatal(err)
	}
	databases["sqlite"] = sqliteDB
	errorCodeFuncs["sqlite"] = func(err error) (errcode string) {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) {
			return strconv.Itoa(int(sqliteErr.Code()))
		}
		return ""
	}
	if *postgresDSN != "" {
		postgresDB, err := sql.Open("postgres", *postgresDSN)
		if err != nil {
			log.Fatal(err)
		}
		wipeCmd := ddl.WipeCmd{
			DB:      postgresDB,
			Dialect: ddl.DialectPostgres,
		}
		err = wipeCmd.Run()
		if err != nil {
			log.Fatal(err)
		}
		err = automigrate(ddl.DialectPostgres, postgresDB)
		if err != nil {
			log.Fatal(err)
		}
		databases["postgres"] = postgresDB
		errorCodeFuncs["postgres"] = func(err error) (errcode string) {
			var pqerr *pq.Error
			if errors.As(err, &pqerr) {
				return string(pqerr.Code)
			}
			return ""
		}
	}
	if *mysqlDSN != "" {
		mysqlDB, err := sql.Open("mysql", *mysqlDSN)
		if err != nil {
			log.Fatal(err)
		}
		wipeCmd := ddl.WipeCmd{
			DB:      mysqlDB,
			Dialect: ddl.DialectMySQL,
		}
		err = wipeCmd.Run()
		if err != nil {
			log.Fatal(err)
		}
		err = automigrate(ddl.DialectMySQL, mysqlDB)
		if err != nil {
			log.Fatal(err)
		}
		databases["mysql"] = mysqlDB
		errorCodeFuncs["mysql"] = func(err error) (errcode string) {
			var mysqlerr *mysql.MySQLError
			if errors.As(err, &mysqlerr) {
				return strconv.FormatUint(uint64(mysqlerr.Number), 10)
			}
			return ""
		}
	}
	logger := sq.NewLogger(os.Stdout, "", log.LstdFlags, sq.LoggerConfig{
		ShowTimeTaken:      true,
		ShowCaller:         true,
		InterpolateVerbose: true,
	})
	sq.SetDefaultLogQuery(func(ctx context.Context, queryStats sq.QueryStats) {
		if queryStats.Err != nil {
			if errors.Is(queryStats.Err, context.Canceled) {
				return
			}
			logger.SqLogQuery(ctx, queryStats)
		}
	})
	code := m.Run()
	for _, db := range databases {
		db.Close()
	}
	os.Exit(code)
}
