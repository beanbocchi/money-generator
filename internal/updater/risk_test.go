package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var defaultRisk = regexp.MustCompile(defRiskPattern)

func TestRiskSnapshot(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"strategy shape", strategy("True", "800", calcLine) + "\n", []string{
			"NFI_SAFE_PARAMETERS has futures_mode_leverage",
			"NFI_SAFE_PARAMETERS has num_cores_indicators_calc",
			"stoploss = -0.99",
			"system_v3_2_stops_enable = True",
		}},
		{"other class ignored", "class Cache:\n  stoploss = 1\nclass NostalgiaForInfinityX7:\n  stoploss = 2\n", []string{"stoploss = 2"}},
		{"only two-space indent counts", "class NostalgiaForInfinityX7(I):\n   stoploss = 1\n\tstoploss = 2\n  stoploss = 3\n", []string{"stoploss = 3"}},
		{"name must match", "class NostalgiaForInfinityX7(I):\n  timeframe = \"5m\"\n  use_exit_signal = True\n", nil},
		{"multi-line joined", "class NostalgiaForInfinityX7(I):\n  grind_mode_coins = [\n    \"BTC\",\t\"ETH\",\n    \"DOGE\",  # c\n  ]\n", []string{`grind_mode_coins = [ "BTC", "ETH", "DOGE", # c ]`}},
		{"unindented continuation", "class NostalgiaForInfinityX7(I):\n  stop_list = [\n\"a\",\n]\n", []string{`stop_list = [ "a", ]`}},
		{"nested brackets", "class NostalgiaForInfinityX7(I):\n  minimal_roi = {\n    \"0\": (1, [2]),\n  }\n", []string{`minimal_roi = { "0": (1, [2]), }`}},
		{"class line ends a join", "class NostalgiaForInfinityX7(I):\n  stop_list = [\n    1,\nclass Next:\n  ]\n", nil},
		{"safe list inside a join", "class NostalgiaForInfinityX7(I):\n  stop_list = [\n    NFI_SAFE_PARAMETERS = [\n      \"a\",\n    ]\n  ]\n",
			[]string{"NFI_SAFE_PARAMETERS has a", "stop_list = [ ]"}},
		{"first quoted string only", "class NostalgiaForInfinityX7(I):\n    NFI_SAFE_PARAMETERS = [\n      \"\", \"x\" \"y\",\n      none,\n  ],\n  stoploss = 1\n",
			[]string{"NFI_SAFE_PARAMETERS has , ", "stoploss = 1"}},
		{"closing bracket first", "class NostalgiaForInfinityX7(I):\n  stoploss = )\n", []string{"stoploss = )"}},
		{"inner spacing kept on one line", "class NostalgiaForInfinityX7(I):\n  stoploss =   -0.99\t# x\n", []string{"stoploss =   -0.99\t# x"}},
		{"crlf kept", "class NostalgiaForInfinityX7(I):\r\n  stoploss = 1\r\n", []string{"stoploss = 1\r"}},
		{"no final newline", "class NostalgiaForInfinityX7(I):\n  stoploss = 1", []string{"stoploss = 1"}},
		{"duplicates kept, sorted bytewise", "class NostalgiaForInfinityX7(I):\n  stoploss = a\n  stoploss = a\n  stoploss = B\n  Stop_a = 2\n", []string{"stoploss = B", "stoploss = a", "stoploss = a"}},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := riskSnapshot(tt.src, "NostalgiaForInfinityX7", defaultRisk)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestRiskDiff(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want []string
	}{
		{"same", []string{"a = 1"}, []string{"a = 1"}, nil},
		{"changed value", []string{"s = True"}, []string{"s = False"}, []string{"+ s = False", "- s = True"}},
		{"duplicates pair off", []string{"x", "x", "y"}, []string{"x", "y"}, []string{"- x"}},
		{"cut at a tab", []string{"s = 1\t# a"}, []string{"s = 1\t# b"}, []string{"- s = 1", "+ s = 1"}},
		{"added", nil, []string{"a", "b"}, []string{"+ a", "+ b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := riskDiff(tt.a, tt.b); strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRiskDigestVector(t *testing.T) {
	// From update.sh: printf '%s\n' "$rdiff" | sha256sum | cut -c1-16.
	diff := []string{"+ system_v3_2_stops_enable = False", "- system_v3_2_stops_enable = True"}
	if got := riskDigest(diff); got != "42fc18ce542d59be" {
		t.Errorf("digest %s", got)
	}
}

func TestPendingDigest(t *testing.T) {
	for content, want := range map[string]string{
		"commit 0123abcd -> 4567ef01  digest 42fc18ce542d59be\n+ x\n": "42fc18ce542d59be",
		"commit a -> b  digest \n":                                    "",
		"+ x\ndigest abc\n":                                           "",
		"":                                                            "",
	} {
		if got := pendingDigest(content); got != want {
			t.Errorf("pendingDigest(%q) = %q, want %q", content, got, want)
		}
	}
}

// shellRisk runs the bash updater's risk_snapshot and risk_diff (frozen in
// testdata/risk.bash) against the repo at nfi. It skips the test when bash or awk are
// missing.
func shellRisk(t *testing.T, nfi, script string) string {
	t.Helper()
	src := filepath.Join("testdata", "risk.bash")
	for _, bin := range []string{"bash", "awk", "sha256sum", "comm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not on PATH")
		}
	}
	cmd := exec.Command("bash", "-c", `set -uo pipefail
source "$SRC"
`+script)
	cmd.Env = append(os.Environ(), "SRC="+src, "NFI_DIR="+nfi, "STRATEGY_FILE=NostalgiaForInfinityX7.py",
		"RISK_PATTERN="+defRiskPattern, "LC_ALL=C.UTF-8")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	return string(out)
}

// goSnapshotAt runs riskSnapshot on the strategy at rev, read with git show.
func goSnapshotAt(t *testing.T, nfi, rev string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", nfi, "show", rev+":NostalgiaForInfinityX7.py").Output()
	if err != nil {
		t.Fatalf("git show %s: %v", rev, err)
	}
	return riskSnapshot(string(out), "NostalgiaForInfinityX7", defaultRisk)
}

// compareWithShell checks snapshot, diff and digest against the shell for each rev and
// each consecutive pair.
func compareWithShell(t *testing.T, nfi string, revs []string) {
	t.Helper()
	for i, rev := range revs {
		want := shellRisk(t, nfi, `risk_snapshot "`+rev+`"`)
		got := goSnapshotAt(t, nfi, rev)
		gotText := strings.Join(got, "\n")
		if len(got) > 0 {
			gotText += "\n"
		}
		if gotText != want {
			t.Errorf("snapshot at %s differs from the shell's\n%s", rev, firstDifference(gotText, want))
		} else {
			t.Logf("snapshot at %s: %d lines, identical", rev, len(got))
		}
		if i == 0 {
			continue
		}
		prev := revs[i-1]
		wantDiff := shellRisk(t, nfi, `d=$(risk_diff "`+prev+`" "`+rev+`"); [ -n "$d" ] && { printf '%s\n' "$d"; printf '%s\n' "$d" | sha256sum | cut -c1-16; }; true`)
		diff := riskDiff(goSnapshotAt(t, nfi, prev), got)
		gotDiff := ""
		if len(diff) > 0 {
			gotDiff = strings.Join(diff, "\n") + "\n" + riskDigest(diff) + "\n"
		}
		if gotDiff != wantDiff {
			t.Errorf("diff %s..%s differs from the shell's\n%s", prev, rev, firstDifference(gotDiff, wantDiff))
		}
	}
}

func firstDifference(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < max(len(g), len(w)); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			return fmt.Sprintf("line %d:\n  go:    %q\n  shell: %q", i+1, gl, wl)
		}
	}
	return "(lengths differ)"
}

func TestRiskSnapshotMatchesShellOnFixtures(t *testing.T) {
	f := newFixture(t)
	fixtures := []string{
		strategy("True", "800", calcLine),
		strategy("False", "800", calcLine),
		listed("DOGE"),
		listed("PEPE"),
		"class NostalgiaForInfinityX7(I):\n  stop_list = [\n    NFI_SAFE_PARAMETERS = [\n      \"a\",\n    ]\n  ]\n  stoploss =   -0.99\t# x\n",
		"class NostalgiaForInfinityX7(I):\n  stoploss =   -0.99\t# y\n  grind_x = {\n\t\"a\":  (1,\n  2)}\nclass Next:\n  stoploss = 5\n",
		"class Cache:\n  stoploss = 1\nclass NostalgiaForInfinityX7:\n  stoploss = 2\n  stoploss = 2\n  Stop_a = 2\n   stop_b = 1\n",
		"class NostalgiaForInfinityX7(I):\r\n  stoploss = 1\r\n    NFI_SAFE_PARAMETERS = [\r\n      \"\", \"x\" \"y\",\r\n  ]\r\n",
		"class NostalgiaForInfinityX7(I):\n  derisk_é = \"ü\"\n  stop_list = [\n    1,\nclass Next:\n  ]\n",
		"class NostalgiaForInfinityX7(I):\n  stop_list = [\n\"a\",\n]\n",
		"no strategy class here\n  stoploss = 1\n",
	}
	revs := []string{}
	for _, src := range fixtures {
		writeFile(t, filepath.Join(f.upstream, "NostalgiaForInfinityX7.py"), src)
		gitT(t, f.upstream, "commit", "-qam", "fixture")
		revs = append(revs, gitT(t, f.upstream, "rev-parse", "HEAD"))
	}
	compareWithShell(t, f.upstream, revs)
}

// TestRiskSnapshotMatchesShellOnRealClone compares against the shell on the real
// NostalgiaForInfinityX7.py in the repo's clone, using only git show (read-only).
func TestRiskSnapshotMatchesShellOnRealClone(t *testing.T) {
	nfi := filepath.Join("..", "..", "NostalgiaForInfinity")
	if testing.Short() {
		t.Skip("-short")
	}
	if _, err := os.Stat(filepath.Join(nfi, ".git")); err != nil {
		t.Skip("no NostalgiaForInfinity clone")
	}
	out, err := exec.Command("git", "-C", nfi, "log", "--format=%H", "-n", "3000", "--", "NostalgiaForInfinityX7.py").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	all := strings.Fields(string(out))
	var revs []string
	for _, i := range []int{2999, 2500, 1000, 300, 50, 2, 1, 0} { // oldest first
		if i < len(all) {
			revs = append(revs, all[i])
		}
	}
	// 54ab6f2f6 is the commit that switched the stop off (ADR-0010); its parent and it
	// must give a non-empty diff that names system_v3_2_stops_enable.
	if p, err := exec.Command("git", "-C", nfi, "rev-parse", "-q", "--verify", "54ab6f2f6^{commit}").Output(); err == nil {
		c := strings.TrimSpace(string(p))
		compareWithShell(t, nfi, []string{c + "^", c})
		diff := riskDiff(goSnapshotAt(t, nfi, c+"^"), goSnapshotAt(t, nfi, c))
		if !strings.Contains(strings.Join(diff, "\n"), "system_v3_2_stops_enable") {
			t.Errorf("54ab6f2f6 not held by the fuse: %q", diff)
		}
	}
	compareWithShell(t, nfi, revs)
}
