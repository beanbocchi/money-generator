package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// fakeRunner records commands. A clone creates the directory it would produce, like
// the git stub in tests/test_setup.sh. fail makes the named subcommand fail.
type fakeRunner struct {
	mu    sync.Mutex
	root  string
	calls [][]string
	fail  string // "clone", "up" or "ps"
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	argv := append([]string{name}, args...)
	f.mu.Lock()
	f.calls = append(f.calls, argv)
	f.mu.Unlock()
	for _, a := range args {
		if a == f.fail {
			return "", errors.New(a + " failed")
		}
	}
	if len(args) > 0 && args[0] == "clone" {
		if err := os.MkdirAll(filepath.Join(f.root, args[len(args)-1], ".git"), 0o755); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (f *fakeRunner) Calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

// fixture is a throwaway project holding only what setup needs: the repo's real
// .env.example, copied in as tests/test_setup.sh did.
func fixture(t *testing.T, env map[string]string) (Config, *fakeRunner, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	b, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.example"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"NFI_REPO", "GIT", "COMPOSE"} {
		t.Setenv(k, env[k]) // empty means the default, so the host's own values stay out
	}
	f := &fakeRunner{root: root}
	var out bytes.Buffer
	return Config{
		Root:      root,
		UID:       1234,
		GID:       5678,
		DockerGID: func() string { return "4242" },
		Stdout:    &out,
		Stderr:    &out,
		Runner:    f,
	}, f, &out
}

// configure writes a .env with every empty value filled, as test_setup.sh's sed did.
func configure(t *testing.T, c Config) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(c.Root, ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	filled := regexp.MustCompile(`(?m)^([A-Z_]*)=$`).ReplaceAll(b, []byte("${1}=filled"))
	if err := os.WriteFile(filepath.Join(c.Root, ".env"), filled, 0o600); err != nil {
		t.Fatal(err)
	}
	return filled
}

func has(calls [][]string, word string) bool {
	for _, c := range calls {
		for _, a := range c {
			if a == word {
				return true
			}
		}
	}
	return false
}

func equalArgv(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv\n got: %q\nwant: %q", got, want)
	}
}

func dirExists(path string) bool { fi, err := os.Stat(path); return err == nil && fi.IsDir() }

// test_setup.sh case 1.
func TestNoEnvCreatesItAndRefusesToStart(t *testing.T) {
	c, f, out := fixture(t, nil)
	if rc := Run(context.Background(), c); rc == 0 {
		t.Fatal("exited 0 with an unconfigured .env")
	}
	envPath := filepath.Join(c.Root, ".env")
	b, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal("did not create .env")
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls %q, want only the clone", calls)
	}
	equalArgv(t, calls[0], []string{"git", "clone", "--filter=blob:none", "--single-branch", "-b", "main",
		"https://github.com/iterativv/NostalgiaForInfinity", "NostalgiaForInfinity"})
	if has(calls, "up") {
		t.Fatal("started the stack without configuration")
	}
	if !regexp.MustCompile(`(?m)^\d+:FREQTRADE__EXCHANGE__KEY=$`).MatchString(out.String()) {
		t.Errorf("did not name the empty variables with line numbers:\n%s", out)
	}
	for _, d := range []string{"user_data/logs", "user_data/data", "services/updater/state"} {
		if !dirExists(filepath.Join(c.Root, d)) {
			t.Errorf("missing %s", d)
		}
	}
	for _, line := range []string{"HOST_UID=1234", "HOST_GID=5678", "DOCKER_GID=4242"} {
		if !regexp.MustCompile(`(?m)^` + line + `$`).Match(b) {
			t.Errorf(".env lacks %q", line)
		}
	}
	if !strings.Contains(out.String(), "==> set HOST_UID=1234 HOST_GID=5678 DOCKER_GID=4242 in .env from this host") {
		t.Errorf("did not report the ids:\n%s", out)
	}
}

func TestNewEnvKeepsEverythingElse(t *testing.T) {
	c, _, _ := fixture(t, nil)
	Run(context.Background(), c)
	example, _ := os.ReadFile(filepath.Join(c.Root, ".env.example"))
	env, err := os.ReadFile(filepath.Join(c.Root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	want := setKeys(string(example), map[string]string{"HOST_UID": "1234", "HOST_GID": "5678", "DOCKER_GID": "4242"})
	if string(env) != want {
		t.Fatal(".env differs from .env.example beyond the three ids")
	}
	ex := strings.Split(string(example), "\n")
	got := strings.Split(string(env), "\n")
	if len(ex) != len(got) {
		t.Fatalf(".env has %d lines, .env.example %d", len(got), len(ex))
	}
	changed := 0
	for i := range ex {
		if ex[i] != got[i] {
			changed++
		}
	}
	if changed != 3 {
		t.Fatalf("%d lines changed, want 3", changed)
	}
}

func TestNoDockerGroupWarnsAndKeepsExampleValue(t *testing.T) {
	c, _, out := fixture(t, nil)
	c.DockerGID = func() string { return "" }
	if rc := Run(context.Background(), c); rc == 0 {
		t.Fatal("exited 0")
	}
	if !strings.Contains(out.String(), "==> WARNING: 'getent group docker' found no docker group") {
		t.Errorf("no warning:\n%s", out)
	}
	b, _ := os.ReadFile(filepath.Join(c.Root, ".env"))
	if !regexp.MustCompile(`(?m)^DOCKER_GID=968$`).Match(b) || !regexp.MustCompile(`(?m)^HOST_UID=1234$`).Match(b) {
		t.Errorf(".env ids wrong:\n%s", b)
	}
}

// test_setup.sh case 2.
func TestConfiguredEnvStartsTheStack(t *testing.T) {
	c, f, out := fixture(t, nil)
	before := configure(t, c)
	if rc := Run(context.Background(), c); rc != 0 {
		t.Fatalf("rc = %d with a configured .env:\n%s", rc, out)
	}
	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls %q, want clone, up, ps", calls)
	}
	equalArgv(t, calls[1], []string{"docker", "compose", "up", "-d", "--build"})
	equalArgv(t, calls[2], []string{"docker", "compose", "ps"})
	if !strings.Contains(out.String(), "==> starting stack\n") {
		t.Errorf("no banner:\n%s", out)
	}
	after, _ := os.ReadFile(filepath.Join(c.Root, ".env"))
	if !bytes.Equal(before, after) {
		t.Fatal("changed an existing .env")
	}
}

// test_setup.sh case 3.
func TestExistingCloneIsNotRecloned(t *testing.T) {
	c, f, out := fixture(t, nil)
	if err := os.MkdirAll(filepath.Join(c.Root, "NostalgiaForInfinity/.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	configure(t, c)
	if rc := Run(context.Background(), c); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if has(f.Calls(), "clone") {
		t.Fatal("re-cloned over an existing checkout")
	}
	if !strings.Contains(out.String(), "==> NostalgiaForInfinity already present, skipping clone") {
		t.Errorf("no skip message:\n%s", out)
	}
}

func TestEnvOverridesSplitLikeTheShell(t *testing.T) {
	c, f, _ := fixture(t, map[string]string{
		"GIT":      "/opt/git/bin/git -c protocol.version=2",
		"COMPOSE":  "docker  compose -f compose.yml",
		"NFI_REPO": "https://example.invalid/nfi",
	})
	configure(t, c)
	if rc := Run(context.Background(), c); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	calls := f.Calls()
	equalArgv(t, calls[0], []string{"/opt/git/bin/git", "-c", "protocol.version=2", "clone", "--filter=blob:none",
		"--single-branch", "-b", "main", "https://example.invalid/nfi", "NostalgiaForInfinity"})
	equalArgv(t, calls[1], []string{"docker", "compose", "-f", "compose.yml", "up", "-d", "--build"})
	equalArgv(t, calls[2], []string{"docker", "compose", "-f", "compose.yml", "ps"})
}

func TestCloneFailureStopsBeforeEnv(t *testing.T) {
	c, f, _ := fixture(t, nil)
	f.fail = "clone"
	if rc := Run(context.Background(), c); rc == 0 {
		t.Fatal("exited 0 after a failed clone")
	}
	if _, err := os.Stat(filepath.Join(c.Root, ".env")); err == nil {
		t.Fatal("created .env after a failed clone")
	}
	if len(f.Calls()) != 1 {
		t.Fatalf("calls after a failed clone: %q", f.Calls())
	}
}

func TestComposeUpFailureSkipsPs(t *testing.T) {
	c, f, _ := fixture(t, nil)
	configure(t, c)
	f.fail = "up"
	if rc := Run(context.Background(), c); rc == 0 {
		t.Fatal("exited 0 after compose up failed")
	}
	if has(f.Calls(), "ps") {
		t.Fatal("ran compose ps after compose up failed")
	}
}

func TestMissingExampleIsAnError(t *testing.T) {
	c, f, _ := fixture(t, nil)
	if err := os.Remove(filepath.Join(c.Root, ".env.example")); err != nil {
		t.Fatal(err)
	}
	if rc := Run(context.Background(), c); rc == 0 {
		t.Fatal("exited 0 without .env.example")
	}
	if has(f.Calls(), "up") {
		t.Fatal("started the stack")
	}
}

func TestSetKeysReplacesWholeLinesOnly(t *testing.T) {
	in := "HOST_UID=1000\nXHOST_UID=1\n# HOST_UID=1000\nHOST_UID=\nHOST_GID=1000"
	got := setKeys(in, map[string]string{"HOST_UID": "7"})
	want := "HOST_UID=7\nXHOST_UID=1\n# HOST_UID=1000\nHOST_UID=7\nHOST_GID=1000"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEmptyKeys(t *testing.T) {
	got := emptyKeys("A=1\nB=\n# C=\n\nD= \nE=")
	want := []string{"2:B=", "3:# C=", "6:E="}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}
