//go:build integration || e2e

// Shared file: carried verbatim; never localized outside its source repository.
// Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md.

// Package testdb gives narrow integration tests (build tag integration, run by make integration) and end-to-end tests
// (build tag e2e, run by make e2e) real databases on every dialect the repository keeps schema scripts for. It is
// test-only: the build tags keep it out of every other build.
package testdb

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
	"gorm.io/gorm/logger"
)

// dsnVars names the variable that points at each dialect's test server.
var dsnVars = map[string]string{
	"mysql":    "TEST_MYSQL_DSN",
	"postgres": "TEST_POSTGRES_DSN",
}

// ForEachDB runs fn once per dialect the repository runs on, as a subtest named after the dialect: the dialects every
// migrations/<database>/ has a <dialect>/0_schema.sql for. A repository on one database deletes the other dialect's
// directories from the databases it owns, and the tests follow; a directory the kernel owns keeps both and does not
// widen the set. Each run gets a fresh database on that dialect's server, built from
// migrations/<database>/<dialect>/0_schema.sql of each database in databases (every database when databases is
// empty), as an operator builds a new database, and dropped afterwards. A missing variable fails the test.
func ForEachDB(t *testing.T, databases []string, fn func(t *testing.T, db *gorm.DB)) {
	t.Helper()
	dialects, scripts := schemaScripts(t, databases)
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
			fn(t, db)
		})
	}
}

// schemaScripts returns the dialects every database directory has, sorted, and per dialect the
// migrations/<database>/<dialect>/0_schema.sql of the databases asked for, sorted by database.
func schemaScripts(t *testing.T, databases []string) ([]string, map[string][]string) {
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
	want := map[string]bool{}
	for _, d := range databases {
		want[d] = true
	}
	has := map[string]map[string]bool{} // dialect -> databases that have it
	all := map[string]bool{}
	for _, s := range found {
		dialect := filepath.Base(filepath.Dir(s))
		database := filepath.Base(filepath.Dir(filepath.Dir(s)))
		if _, ok := dsnVars[dialect]; !ok {
			t.Fatalf("%s: unknown dialect directory %q; dialect directories are named after database.<name>.driver: mysql or postgres", s, dialect)
		}
		all[database] = true
		if has[dialect] == nil {
			has[dialect] = map[string]bool{}
		}
		has[dialect][database] = true
	}
	var dialects []string
	scripts := map[string][]string{}
	for dialect, dbs := range has {
		if len(dbs) != len(all) {
			continue // some database does not run on this dialect
		}
		dialects = append(dialects, dialect)
		for _, s := range found {
			database := filepath.Base(filepath.Dir(filepath.Dir(s)))
			if filepath.Base(filepath.Dir(s)) == dialect && (len(want) == 0 || want[database]) {
				scripts[dialect] = append(scripts[dialect], s)
			}
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
	admin, err := gorm.Open(server, &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(fresh, &gorm.Config{TranslateError: true, Logger: logger.Discard})
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
