// Package setup bootstraps the project on a machine (was bin/setup.sh). It is
// idempotent and safe to re-run.
//
// It clones upstream NostalgiaForInfinity, creates the directories docker compose
// would otherwise create as root, and creates .env from .env.example. When it has
// just created .env it stops so the user can fill it in; once .env exists it starts
// the stack.
package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/beanbocchi/money-generator/internal/envfile"
	"github.com/beanbocchi/money-generator/internal/repo"
	"github.com/beanbocchi/money-generator/internal/runx"
)

// Config is everything setup reads from outside the process.
type Config struct {
	Root string // repo root
	UID  int
	GID  int
	// DockerGID returns the docker group's id, or "" when the host has none.
	DockerGID func() string
	Stdout    io.Writer
	Stderr    io.Writer
	Runner    runx.Runner // runs git and docker compose with Root as working directory
}

// Main prepares a fresh machine (was bin/setup.sh).
//
//	mg setup
//
// Environment: NFI_REPO (default https://github.com/iterativv/NostalgiaForInfinity),
// GIT (default git), COMPOSE (default "docker compose").
func Main(ctx context.Context, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(os.Stderr, "usage: mg setup   (NFI_REPO, GIT, COMPOSE from the environment)")
		return 2
	}
	root, err := repo.Root(".env.example")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mg setup: %v\n", err)
		return 1
	}
	c := Config{
		Root:      root,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		DockerGID: hostDockerGID,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		// Combined streams git's and compose's output to the terminal as it comes, and
		// keeps only its last lines for the error instead of a whole build log.
		Runner: runx.Exec{Dir: root, Combined: true, Stdout: os.Stdout, Grace: 30 * time.Second},
	}
	return Run(ctx, c)
}

// errUnconfigured means setup created .env and the user has to fill it in.
var errUnconfigured = errors.New(".env was just created")

// Run performs the setup steps in order and returns the exit status.
func Run(ctx context.Context, c Config) int {
	err := run(ctx, c)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUnconfigured):
		return 1
	default:
		fmt.Fprintf(c.Stderr, "mg setup: %v\n", err)
		return runx.ExitCode(err)
	}
}

func run(ctx context.Context, c Config) error {
	if err := cloneUpstream(ctx, c); err != nil {
		return err
	}
	if err := makeDirs(c.Root); err != nil {
		return err
	}
	created, err := createEnv(c)
	if err != nil {
		return err
	}
	if created {
		return errUnconfigured
	}
	return startStack(ctx, c)
}

// command splits a GIT or COMPOSE value into a program and leading arguments, as the
// unquoted $GIT and $COMPOSE did.
func command(key, def string, args ...string) (string, []string) {
	f := strings.Fields(envfile.Or(key, def))
	if len(f) == 0 { // a value of only spaces
		f = strings.Fields(def)
	}
	return f[0], append(f[1:], args...)
}

const nfiDir = "NostalgiaForInfinity"

// cloneUpstream clones NFI unless NostalgiaForInfinity/.git exists.
//
// The clone is partial. Upstream's .git is about 295MB, mostly old revisions of
// multi-megabyte generated .py files, against a 17MB working tree. blob:none fetches
// only the blobs the checkout needs and keeps the full commit graph, which the
// updater's `git diff OLD NEW` needs. A shallow --depth clone breaks that diff between
// successive fetches.
func cloneUpstream(ctx context.Context, c Config) error {
	if fi, err := os.Stat(filepath.Join(c.Root, nfiDir, ".git")); err == nil && fi.IsDir() {
		fmt.Fprintf(c.Stdout, "==> %s already present, skipping clone\n", nfiDir)
		return nil
	}
	fmt.Fprintf(c.Stdout, "==> cloning %s\n", nfiDir)
	name, args := command("GIT", "git", "clone", "--filter=blob:none", "--single-branch", "-b", "main",
		envfile.Or("NFI_REPO", "https://github.com/iterativv/NostalgiaForInfinity"), nfiDir)
	if _, err := c.Runner.Run(ctx, name, args...); err != nil {
		return fmt.Errorf("clone upstream: %w", err)
	}
	return nil
}

// makeDirs creates the bind-mount sources before compose first runs. docker compose
// creates a missing bind-mount source as root, and a root-owned
// services/updater/state/previous_sha cannot be deleted from the host without sudo.
// That happened during Task 4's rehearsal.
func makeDirs(root string) error {
	for _, d := range []string{"user_data/logs", "user_data/data", "services/updater/state"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// createEnv writes .env from .env.example when .env does not exist, with this host's
// ids, and tells the user what to fill in. It reports whether it created the file.
//
// .env.example ships one machine's HOST_UID, HOST_GID and DOCKER_GID (1000/1000/968),
// which are wrong anywhere else.
func createEnv(c Config) (bool, error) {
	envPath := filepath.Join(c.Root, ".env")
	if _, err := os.Stat(envPath); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("stat .env: %w", err)
	}
	example := filepath.Join(c.Root, ".env.example")
	b, err := os.ReadFile(example)
	if err != nil {
		return false, fmt.Errorf("read .env.example: %w", err)
	}
	fi, err := os.Stat(example)
	if err != nil {
		return false, fmt.Errorf("stat .env.example: %w", err)
	}

	set := map[string]string{"HOST_UID": strconv.Itoa(c.UID), "HOST_GID": strconv.Itoa(c.GID)}
	dockerGID := c.DockerGID()
	if dockerGID != "" {
		set["DOCKER_GID"] = dockerGID
	}
	content := setKeys(string(b), set)

	// O_EXCL: never overwrite an .env that appeared since the check above.
	f, err := os.OpenFile(envPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return false, fmt.Errorf("create .env: %w", err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return false, fmt.Errorf("write .env: %w", err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("write .env: %w", err)
	}

	w := c.Stdout
	if dockerGID != "" {
		fmt.Fprintf(w, "==> set HOST_UID=%d HOST_GID=%d DOCKER_GID=%s in .env from this host\n", c.UID, c.GID, dockerGID)
	} else {
		fmt.Fprintln(w, "==> WARNING: 'getent group docker' found no docker group on this host.")
		fmt.Fprintln(w, "    Leaving DOCKER_GID unset in .env is NOT safe to ignore: an empty")
		fmt.Fprintln(w, "    DOCKER_GID makes the updater unable to reach the docker socket.")
		fmt.Fprintln(w, "    Add the docker group first, or set DOCKER_GID in .env by hand.")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "==> created .env from .env.example. Fill these in, then re-run mg setup:")
	for _, l := range emptyKeys(content) {
		fmt.Fprintln(w, l)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "    FREQTRADE__DRY_RUN is true. Set it to false only when you mean real money.")
	fmt.Fprintln(w, "    To move a live bot here, also copy its *.sqlite* and nfi*-profit_max-*.json")
	fmt.Fprintln(w, "    into user_data/ before starting (see README.md).")
	return true, nil
}

// setKeys replaces every line starting with KEY= for each key in set, like
// sed -i "s/^KEY=.*/KEY=value/".
func setKeys(content string, set map[string]string) string {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		k, _, ok := strings.Cut(l, "=")
		if v, want := set[k]; ok && want {
			lines[i] = k + "=" + v
		}
	}
	return strings.Join(lines, "\n")
}

// emptyKeys lists lines ending in '=' with their line numbers, as grep -n '=$' does.
func emptyKeys(content string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(content))
	for n := 1; sc.Scan(); n++ {
		if strings.HasSuffix(sc.Text(), "=") {
			out = append(out, fmt.Sprintf("%d:%s", n, sc.Text()))
		}
	}
	return out
}

// startStack runs `docker compose up -d --build` and then `docker compose ps`.
func startStack(ctx context.Context, c Config) error {
	fmt.Fprintln(c.Stdout, "==> starting stack")
	for _, sub := range [][]string{{"up", "-d", "--build"}, {"ps"}} {
		name, args := command("COMPOSE", "docker compose", sub...)
		if _, err := c.Runner.Run(ctx, name, args...); err != nil {
			return fmt.Errorf("compose %s: %w", sub[0], err)
		}
	}
	return nil
}

// hostDockerGID looks up the docker group, standing in for
// `getent group docker | cut -d: -f3`. Any failure means no group.
func hostDockerGID() string {
	g, err := user.LookupGroup("docker")
	if err != nil {
		return ""
	}
	return g.Gid
}
