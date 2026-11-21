package sqlitex

import (
	"path/filepath"
	"testing"
)

func TestOpenReadOnlyRefusesWrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.sqlite")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("create table x(a int); insert into x values (1)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ro, err := OpenReadOnly(p)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var n int
	if err := ro.QueryRow("select count(*) from x").Scan(&n); err != nil || n != 1 {
		t.Fatalf("read: n=%d err=%v", n, err)
	}
	if _, err := ro.Exec("insert into x values (2)"); err == nil {
		t.Fatal("write through a read-only connection succeeded")
	}
}
