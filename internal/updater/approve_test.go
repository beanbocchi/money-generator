package updater

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

const samplePending = "commit 0123abcd -> 4567ef01  digest 42fc18ce542d59be\n+ system_v3_2_stops_enable = False\n- system_v3_2_stops_enable = True\n"

func runApprove(t *testing.T, state string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errw strings.Builder
	rc := approve(stateDir(state), args, strings.NewReader(stdin), &out, &errw)
	return rc, out.String(), errw.String()
}

func TestApprove(t *testing.T) {
	tests := []struct {
		name     string
		pending  *string // nil: no file
		stdin    string
		args     []string
		wantRC   int
		wantOut  string
		approved string // expected approved_risk content, "" for none
	}{
		// case 13
		{"nothing pending", nil, "", []string{"--yes"}, 1, "nothing pending: the updater is not holding any update", ""},
		{"empty pending file", ptr(""), "", []string{"--yes"}, 1, "nothing pending", ""},
		{"no digest", ptr("garbage\n"), "", []string{"--yes"}, 1, "cannot read the digest from", ""},
		{"--yes", ptr(samplePending), "", []string{"--yes"}, 0, "approved 42fc18ce542d59be. The updater deploys it", "42fc18ce542d59be\n"},
		{"prompt y", ptr(samplePending), "y\n", nil, 0, "approved 42fc18ce542d59be", "42fc18ce542d59be\n"},
		{"prompt yes with spaces", ptr(samplePending), "  yes \n", nil, 0, "approved", "42fc18ce542d59be\n"},
		{"prompt Y", ptr(samplePending), "Y\n", nil, 0, "approved", "42fc18ce542d59be\n"},
		{"prompt default no", ptr(samplePending), "\n", nil, 1, "not approved, nothing changed.", ""},
		{"prompt n", ptr(samplePending), "n\n", nil, 1, "not approved, nothing changed.", ""},
		{"prompt EOF", ptr(samplePending), "", nil, 1, "not approved", ""},
		{"other flag prompts", ptr(samplePending), "no\n", []string{"-y"}, 1, "not approved", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := t.TempDir()
			if tt.pending != nil {
				writeFile(t, filepath.Join(state, "pending_risk"), *tt.pending)
			}
			rc, out, errw := runApprove(t, state, tt.stdin, tt.args...)
			if rc != tt.wantRC || !strings.Contains(out, tt.wantOut) {
				t.Errorf("rc=%d out=%q; want %d containing %q", rc, out, tt.wantRC, tt.wantOut)
			}
			if got := readFile(t, filepath.Join(state, "approved_risk")); got != tt.approved {
				t.Errorf("approved_risk = %q, want %q", got, tt.approved)
			}
			if tt.pending != nil && *tt.pending == samplePending && !strings.HasPrefix(out, samplePending+"\n") {
				t.Errorf("did not show the pending diff first: %q", out)
			}
			prompted := strings.Contains(errw, "Deploy this change to the live bot? Lines starting '-' are what runs now. [y/N] ")
			wantPrompt := tt.pending != nil && pendingDigest(*tt.pending) != "" && (len(tt.args) == 0 || tt.args[0] != "--yes")
			if prompted != wantPrompt {
				t.Errorf("prompted=%v, want %v (stderr %q)", prompted, wantPrompt, errw)
			}
		})
	}
}

func TestApproveFindsStateFromAnyDirectory(t *testing.T) {
	root := t.TempDir()
	t.Chdir(t.TempDir())
	writeFile(t, filepath.Join(root, HostStateDir, "pending_risk"), samplePending)
	writeFile(t, filepath.Join(root, "other/pending_risk"), samplePending)
	for _, tt := range []struct{ stateEnv, wantDir string }{
		{"", HostStateDir},
		{"other", "other"}, // relative STATE_DIR is taken from the repo root
	} {
		setEnv(t, map[string]string{"MG_ROOT": root, "STATE_DIR": tt.stateEnv})
		if rc := Approve(context.Background(), []string{"--yes"}); rc != 0 {
			t.Fatalf("STATE_DIR=%q: rc %d", tt.stateEnv, rc)
		}
		if readFile(t, filepath.Join(root, tt.wantDir, "approved_risk")) != "42fc18ce542d59be\n" {
			t.Errorf("STATE_DIR=%q: not written under %s", tt.stateEnv, tt.wantDir)
		}
	}
	setEnv(t, map[string]string{"MG_ROOT": t.TempDir()})
	if rc := Approve(context.Background(), []string{"--yes"}); rc == 0 {
		t.Error("ran outside a repo")
	}
}

func ptr(s string) *string { return &s }
