package fdverify

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestVerifyConn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()

	var driverConn driver.Conn
	if err := conn.Raw(func(anyConn any) error {
		driverConn = anyConn.(driver.Conn)
		return nil
	}); err != nil {
		t.Fatalf("raw: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := VerifyConn(driverConn, path, info); err != nil {
		t.Fatalf("VerifyConn() error: %v", err)
	}

	otherPath := filepath.Join(dir, "other")
	if err := os.WriteFile(otherPath, []byte("x"), 0600); err != nil {
		t.Fatalf("write other: %v", err)
	}
	otherInfo, err := os.Stat(otherPath)
	if err != nil {
		t.Fatalf("stat other: %v", err)
	}
	if err := VerifyConn(driverConn, path, otherInfo); err == nil {
		t.Fatal("VerifyConn(wrong identity) error = nil, want error")
	}
}
