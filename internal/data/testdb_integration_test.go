//go:build integration

package data

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/go-keel/keel/core/kdb"
)

// dsnVars names the variable that points at each dialect's test server.
var dsnVars = map[string]string{
	"mysql":    "TEST_MYSQL_DSN",
	"postgres": "TEST_POSTGRES_DSN",
}

// forEachDB runs fn once per dialect the repository runs on, as a subtest named after the dialect: the dialects every
// migrations/<database>/ has a <dialect>/0_schema.sql for. Each run gets a fresh database on that dialect's server,
// built from the complete schema of every database (migrations/*/<dialect>/0_schema.sql, as an operator builds a new
// database) and dropped afterwards. A missing variable fails the test.
func forEachDB(t *testing.T, fn func(t *testing.T, db *kdb.DB)) {
	t.Helper()
	dialects, scripts := schemaScripts(t)
	for _, d := range dialects {
		t.Run(d, func(t *testing.T) {
			db := openFresh(t, d)
			for _, s := range scripts[d] {
				b, err := os.ReadFile(s)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(string(b)).Error; err != nil {
					t.Fatalf("apply %s: %v", s, err)
				}
			}
			fn(t, &kdb.DB{DB: db})
		})
	}
}

// schemaScripts returns the dialects every database directory has, sorted, and per dialect its
// migrations/*/<dialect>/0_schema.sql, sorted by database.
func schemaScripts(t *testing.T) ([]string, map[string][]string) {
	t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil; _, err = os.Stat(filepath.Join(root, "go.mod")) {
		if root = filepath.Dir(root); root == filepath.Dir(root) {
			t.Fatal("go.mod not found above the test package")
		}
	}
	found, err := filepath.Glob(filepath.Join(root, "migrations", "*", "*", "0_schema.sql"))
	if err != nil || len(found) == 0 {
		t.Fatalf("no migrations/<database>/<dialect>/0_schema.sql found (%v)", err)
	}
	sort.Strings(found)
	scripts := map[string][]string{}
	databases := map[string]bool{}
	for _, s := range found {
		dialect := filepath.Base(filepath.Dir(s))
		if _, ok := dsnVars[dialect]; !ok {
			t.Fatalf("%s: unknown dialect directory %q; dialect directories are named after database.<name>.driver: mysql or postgres", s, dialect)
		}
		scripts[dialect] = append(scripts[dialect], s)
		databases[filepath.Base(filepath.Dir(filepath.Dir(s)))] = true
	}
	var dialects []string
	for dialect, ss := range scripts {
		if len(ss) == len(databases) { // otherwise some database does not run on this dialect
			dialects = append(dialects, dialect)
		}
	}
	if len(dialects) == 0 {
		t.Fatal("migrations/: no dialect directory is shared by every database; each migrations/<database>/ needs the <dialect>/ the repository runs on")
	}
	sort.Strings(dialects)
	return dialects, scripts
}

// openFresh creates a uniquely named database on the server of dialect's variable, drops it when the test ends, and
// returns a connection to it that translates driver errors as kdb does.
func openFresh(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(dsnVars[dialect])
	if dsn == "" {
		t.Fatalf("%s is not set. make integration needs a real MySQL and PostgreSQL; see docs/guides/testing.md, \"data tests\"", dsnVars[dialect])
	}
	name := fmt.Sprintf("it_%d", time.Now().UnixNano())
	var server, fresh gorm.Dialector
	switch dialect {
	case "mysql":
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			t.Fatalf("TEST_MYSQL_DSN: %v", err)
		}
		cfg.MultiStatements = true
		cfg.ParseTime = true
		cfg.DBName = ""
		server = gormmysql.Open(cfg.FormatDSN())
		cfg.DBName = name
		fresh = gormmysql.Open(cfg.FormatDSN())
	case "postgres":
		u, err := url.Parse(dsn)
		if err != nil || u.Scheme != "postgres" {
			t.Fatalf("TEST_POSTGRES_DSN must be a postgres:// URL (%v)", err)
		}
		server = postgres.Open(u.String())
		u.Path = "/" + name
		fresh = postgres.Open(u.String())
	}
	admin, err := gorm.Open(server, &gorm.Config{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(fresh, &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// PostgreSQL refuses to drop a database with open connections.
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		admin.Exec("DROP DATABASE " + name)
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
