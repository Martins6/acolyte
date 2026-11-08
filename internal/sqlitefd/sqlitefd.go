//go:build darwin || linux

// Package sqlitefd exposes the OS file descriptor that a SQLite connection
// actually uses for its main database. It is intentionally pinned to the
// bundled mattn/go-sqlite3 unix VFS: SQLITE_FCNTL_FILE_POINTER returns the
// unixFile whose fd field follows pMethod, pVfs, and pInode.
package sqlitefd

/*
#include <stdint.h>

typedef struct sqlite3 sqlite3;
typedef struct sqlite3_file sqlite3_file;

extern int sqlite3_file_control(sqlite3*, const char*, int, void*);

static int acolyte_main_fd(sqlite3* db) {
	sqlite3_file* file = 0;
	int rc = sqlite3_file_control(db, "main", 7, &file);
	if (rc != 0 || file == 0) {
		return -1;
	}
	// unixFile: pMethod (8), pVfs (8), pInode (8), h (int).
	return *(int*)((char*)file + 24);
}
*/
import "C"

import (
	"database/sql/driver"
	"fmt"
	"reflect"
)

// MainFDFromConn returns the main database fd for a *sqlite3.SQLiteConn.
func MainFDFromConn(conn driver.Conn) (int, error) {
	value := reflect.ValueOf(conn)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return -1, fmt.Errorf("sqlitefd: not a SQLite connection")
	}
	field := value.Elem().FieldByName("db")
	if !field.IsValid() || field.IsNil() {
		return -1, fmt.Errorf("sqlitefd: connection has no database handle")
	}
	handle := field.UnsafePointer()
	if handle == nil {
		return -1, fmt.Errorf("sqlitefd: nil database handle")
	}

	fd := int(C.acolyte_main_fd((*C.sqlite3)(handle)))
	if fd < 0 {
		return -1, fmt.Errorf("sqlitefd: main database fd unavailable")
	}
	return fd, nil
}
