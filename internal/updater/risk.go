package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// The import guard catches code that is broken. It cannot catch code that is valid and
// changes what the bot risks: upstream commit 54ab6f2f6 (2026-04-19, 19 lines, message
// "X7: use system_v3_2.") set system_v3_2_stops_enable = False and reached real money
// unread. So before deploying, the class-level settings whose name matches RISK_PATTERN
// are compared between the running commit and upstream, plus the membership of
// NFI_SAFE_PARAMETERS. Dropping a name from that list makes freqtrade ignore our config
// override for it; that is how 2x leverage would become upstream's 3x.
//
// The pattern is broad on purpose. Over the 755 X7 commits from 2026-07-30 to
// 2026-10-30 it would have held 2 (on 2 days); a stop/leverage-only pattern held 1.
//
// riskSnapshot reproduces update.sh's awk program line for line, so a digest approved
// by the shell version still matches. The order of the checks in the loop is the order
// of the awk rules, and it matters: a "class" line ends the scan even in the middle of
// a multi-line value, and "NFI_SAFE_PARAMETERS = [" is recognised before the
// multi-line join.

var (
	settingLine = regexp.MustCompile(`^  ([A-Za-z_][A-Za-z0-9_]*) = `)
	safeClose   = regexp.MustCompile(`^[ \t]*\]`)
	firstQuoted = regexp.MustCompile(`"[^"]+"`)
	blankRun    = regexp.MustCompile(`[ \t]+`)
)

// riskSnapshot returns the risk-relevant settings of the strategy source src, sorted
// bytewise (LC_ALL=C sort): "name = value" lines for class-level settings of class
// className whose name matches pat, and one "NFI_SAFE_PARAMETERS has <name>" line per
// overridable parameter.
func riskSnapshot(src, className string, pat *regexp.Regexp) []string {
	// The awk rule was /^class <name>[(:]/.
	classPrefix := "class " + className
	classLine := func(l string) bool {
		rest, ok := strings.CutPrefix(l, classPrefix)
		return ok && rest != "" && (rest[0] == '(' || rest[0] == ':')
	}
	var (
		out                 []string
		inside, safe, multi bool
		joined              string
		depth               int
	)
	for _, l := range records(src) {
		switch {
		case classLine(l):
			inside = true
		case inside && strings.HasPrefix(l, "class "):
			sort.Strings(out)
			return out
		case !inside:
		case strings.Contains(l, "NFI_SAFE_PARAMETERS = ["):
			safe = true
		case safe:
			if safeClose.MatchString(l) {
				safe = false
			} else if q := firstQuoted.FindString(l); q != "" {
				out = append(out, "NFI_SAFE_PARAMETERS has "+q[1:len(q)-1])
			}
		case multi:
			// A value spanning lines (grind_mode_coins = [ ... ]) is joined into one
			// line until its brackets balance, so a change inside the list is seen.
			joined += " " + l
			depth += bracketDepth(l)
			if depth <= 0 {
				multi = false
				out = append(out, blankRun.ReplaceAllString(joined, " "))
			}
		default:
			m := settingLine.FindStringSubmatch(l)
			if m == nil || !pat.MatchString(m[1]) {
				continue
			}
			s := strings.TrimLeft(l, " ")
			if depth = bracketDepth(s); depth > 0 {
				multi, joined = true, s
			} else {
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

// records splits text into lines the way awk reads records: a final newline does not
// start an empty record.
func records(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// bracketDepth counts opening minus closing brackets of all three kinds, including any
// inside strings or comments, as the awk gsub count did.
func bracketDepth(s string) int {
	d := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[', '(', '{':
			d++
		case ']', ')', '}':
			d--
		}
	}
	return d
}

// riskDiff is `comm -3 a b` piped through `awk -F'\t'`: "- line" for a line only in a
// (what runs now), "+ line" for a line only in b (upstream), in merged sorted order.
// Both inputs must be sorted bytewise. Duplicates pair off one for one, as in comm.
//
// The awk printed $1 or $2 of a tab-separated comm line, so a setting whose text holds
// a tab is cut at that tab. That is kept: the digest must match the shell's.
func riskDiff(a, b []string) []string {
	var out []string
	minus := func(s string) { out = append(out, "- "+beforeTab(s)) }
	plus := func(s string) { out = append(out, "+ "+beforeTab(s)) }
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			i++
			j++
		case a[i] < b[j]:
			minus(a[i])
			i++
		default:
			plus(b[j])
			j++
		}
	}
	for ; i < len(a); i++ {
		minus(a[i])
	}
	for ; j < len(b); j++ {
		plus(b[j])
	}
	return out
}

func beforeTab(s string) string {
	before, _, _ := strings.Cut(s, "\t")
	return before
}

// riskDigest is `printf '%s\n' "$rdiff" | sha256sum | cut -c1-16`.
func riskDigest(diff []string) string {
	sum := sha256.Sum256([]byte(strings.Join(diff, "\n") + "\n"))
	return hex.EncodeToString(sum[:])[:16]
}

var digestField = regexp.MustCompile(`digest ([0-9a-f]+)`)

// pendingDigest reads the digest from the first line of a pending_risk file, which is
// "commit <old8> -> <new8>  digest <hex16>". Empty when there is none.
func pendingDigest(content string) string {
	first, _, _ := strings.Cut(content, "\n")
	if m := digestField.FindStringSubmatch(first); m != nil {
		return m[1]
	}
	return ""
}
