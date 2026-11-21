package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	body := "# comment\n\nA=1\nexport B = two \nC=\"quoted value\"\nD='x'\nNOEQ\nA=3\nE=a=b\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "3", "B": "two", "C": "quoted value", "D": "x", "E": "a=b"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	if _, ok := m["NOEQ"]; ok {
		t.Error("line without = was parsed")
	}
}

func TestOrIntSeconds(t *testing.T) {
	t.Setenv("MG_T_EMPTY", "")
	t.Setenv("MG_T_N", " 42 ")
	t.Setenv("MG_T_BAD", "4x")
	if Or("MG_T_EMPTY", "d") != "d" || Or("MG_T_UNSET_XYZ", "d") != "d" {
		t.Error("Or: empty or unset must give the default")
	}
	if n, err := Int("MG_T_N", 1); err != nil || n != 42 {
		t.Errorf("Int = %d, %v", n, err)
	}
	if _, err := Int("MG_T_BAD", 1); err == nil {
		t.Error("Int: want error for 4x")
	}
	if d, err := Seconds("MG_T_UNSET_XYZ", 90); err != nil || d.Seconds() != 90 {
		t.Errorf("Seconds = %v, %v", d, err)
	}
}
