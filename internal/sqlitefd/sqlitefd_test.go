package sqlitefd

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestMainFDFromConn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
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

	var fd int
	if err := conn.Raw(func(driverConn any) error {
		var err error
		fd, err = MainFDFromConn(driverConn.(driver.Conn))
		return err
	}); err != nil {
		t.Fatalf("raw: %v", err)
	}
	if fd < 0 {
		t.Errorf("fd = %d, want >= 0", fd)
	}
}
