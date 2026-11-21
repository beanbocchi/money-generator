package updater

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/beanbocchi/money-generator/internal/repo"
)

// Approve accepts a held risk-setting change (was bin/approve-update.sh). The updater
// deploys it on its next cycle, still behind the restart cap and the volatility
// checks. The approval covers that exact diff: if upstream moves the same settings
// again before it deploys, the update is held again and needs a fresh approval.
//
// It works from any directory. STATE_DIR defaults to services/updater/state under the
// repo root. --yes skips the prompt.
func Approve(_ context.Context, args []string) int {
	root, err := repo.Root("services/updater")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return approve(stateDir(hostDir(root, "STATE_DIR", HostStateDir)), args, os.Stdin, os.Stdout, os.Stderr)
}

func approve(st stateDir, args []string, in io.Reader, out, errw io.Writer) int {
	p := st.Path("pending_risk")
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		fmt.Fprintf(out, "nothing pending: the updater is not holding any update (no %s).\n", p)
		return 1
	}
	fmt.Fprint(out, string(b))
	fmt.Fprintln(out)
	digest := pendingDigest(string(b))
	if digest == "" {
		fmt.Fprintf(out, "cannot read the digest from %s; nothing changed.\n", p)
		return 1
	}

	if len(args) == 0 || args[0] != "--yes" {
		fmt.Fprint(errw, "Deploy this change to the live bot? Lines starting '-' are what runs now. [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.Trim(line, " \t\n") {
		case "y", "Y", "yes":
		default:
			fmt.Fprintln(out, "not approved, nothing changed.")
			return 1
		}
	}
	if err := st.Write("approved_risk", digest); err != nil {
		fmt.Fprintf(errw, "cannot record the approval: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "approved %s. The updater deploys it on its next cycle (up to UPDATE_INTERVAL).\n", digest)
	return 0
}
