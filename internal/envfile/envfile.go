// Package envfile reads KEY=VALUE files such as the repo's .env without exporting them
// into the process environment.
package envfile

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Read parses path. Blank lines and lines starting with # are skipped; surrounding
// single or double quotes are stripped from values. Later keys win.
func Read(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		m[k] = v
	}
	return m, sc.Err()
}

// Get returns the environment variable key if set, else def.
func Get(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// Or returns the environment variable key, or def when it is unset or empty, as bash's
// ${KEY:-def}.
func Or(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Int is Or parsed as an integer; the error names the variable.
func Int(key string, def int) (int, error) {
	v := Or(key, strconv.Itoa(def))
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%s=%q: not an integer", key, v)
	}
	return n, nil
}

// Seconds is Int read as a number of seconds.
func Seconds(key string, def int) (time.Duration, error) {
	n, err := Int(key, def)
	return time.Duration(n) * time.Second, err
}
