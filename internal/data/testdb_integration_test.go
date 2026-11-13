//go:build integration

package data

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/go-keel/keel/core/kdb"
)

// openTestDB creates a fresh database on the MySQL server in TEST_MYSQL_DSN, applies the complete schema of every
// database (migrations/*/0_schema.sql, as an operator builds a new database), and drops the database afterwards.
// A missing variable fails the test.
func openTestDB(t *testing.T) *kdb.DB {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("TEST_MYSQL_DSN is not set. make integration needs a real MySQL, e.g. " +
			"docker run -d --rm --name keel-it-mysql -p 53306:3306 -e MYSQL_ROOT_PASSWORD=keel mysql:8.4 and " +
			"TEST_MYSQL_DSN='root:keel@tcp(127.0.0.1:53306)/?parseTime=true'")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("TEST_MYSQL_DSN: %v", err)
	}
	cfg.MultiStatements = true
	cfg.ParseTime = true
	server := *cfg
	server.DBName = ""
	admin, err := gorm.Open(gormmysql.Open(server.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	name := fmt.Sprintf("it_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP DATABASE " + name) })

	cfg.DBName = name
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil; _, err = os.Stat(filepath.Join(root, "go.mod")) {
		if root = filepath.Dir(root); root == filepath.Dir(root) {
			t.Fatal("go.mod not found above the test package")
		}
	}
	scripts, err := filepath.Glob(filepath.Join(root, "migrations", "*", "0_schema.sql"))
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no migrations/*/0_schema.sql found (%v)", err)
	}
	sort.Strings(scripts)
	for _, s := range scripts {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(string(b)).Error; err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(s), err)
		}
	}
	return &kdb.DB{DB: db}
}
