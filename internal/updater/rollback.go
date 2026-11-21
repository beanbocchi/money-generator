package updater

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/beanbocchi/money-generator/internal/envfile"
	"github.com/beanbocchi/money-generator/internal/repo"
	"github.com/beanbocchi/money-generator/internal/runx"
)

// Rollback pins the clone back one update (was bin/rollback.sh), for a version that
// imports fine but trades badly, which the updater's guards cannot detect. It stops the
// updater first, so the updater's next cycle cannot pull forward again and undo it.
//
// It works from any directory. NFI_DIR (default NostalgiaForInfinity) and STATE_DIR
// (default services/updater/state) are taken from the repo root when relative.
func Rollback(ctx context.Context, _ []string) int {
	root, err := repo.Root("services/updater")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return rollback(ctx, root, runx.Exec{Dir: root, Stdout: os.Stdout}, os.Stdout, os.Stderr)
}

func rollback(ctx context.Context, root string, r runx.Runner, out, errw io.Writer) int {
	nfi := hostDir(root, "NFI_DIR", "NostalgiaForInfinity")
	prev := stateDir(hostDir(root, "STATE_DIR", HostStateDir)).Path("previous_sha")
	docker := envfile.Or("DOCKER", "docker")
	target := envfile.Or("TARGET_CONTAINER", "bottrade")
	updater := envfile.Or("UPDATER_CONTAINER", "bottrade-updater")

	b, err := os.ReadFile(prev)
	if err != nil {
		fmt.Fprintf(out, "no %s - the updater has not applied an update yet.\n", prev)
		fmt.Fprintln(out, "To pin a specific commit by hand:")
		fmt.Fprintf(out, "  %s stop %s\n", docker, updater)
		fmt.Fprintf(out, "  git -C %s reset --hard <sha>   # pick one from git -C %s log\n", nfi, nfi)
		fmt.Fprintf(out, "  %s restart %s\n", docker, target)
		return 1
	}
	sha := strings.TrimRight(string(b), "\n")

	// Validate before touching anything. A truncated or garbage previous_sha would
	// otherwise stop the updater and then fail on the reset. Auto-updates would then be
	// off and the bot still on the version being rolled away from.
	if _, err := r.Run(ctx, "git", "-C", nfi, "cat-file", "-e", sha+"^{commit}"); err != nil {
		fmt.Fprintf(out, "%s does not name a commit in %s: '%s'\n", prev, nfi, sha)
		fmt.Fprintf(out, "Nothing was changed. Pick a commit by hand from: git -C %s log --oneline\n", nfi)
		return 1
	}

	fmt.Fprintf(out, "==> stopping %s so it cannot pull forward\n", updater)
	if _, err := r.Run(ctx, docker, "stop", updater); err != nil {
		fmt.Fprintln(errw, err) // carry on, as `|| true` did: it may already be stopped
	}

	fmt.Fprintf(out, "==> pinning %s to %s\n", nfi, sha)
	if _, err := r.Run(ctx, "git", "-C", nfi, "reset", "--hard", "-q", sha); err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}

	fmt.Fprintf(out, "==> restarting %s\n", target)
	if _, err := r.Run(ctx, docker, "restart", target); err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Bot now runs %s. The updater is STOPPED, so this sticks.\n", short(sha))
	fmt.Fprintf(out, "Re-enable auto-updates with: %s start %s\n", docker, updater)
	fmt.Fprintln(out, "(it will pull to latest again on its next cycle)")
	return 0
}
