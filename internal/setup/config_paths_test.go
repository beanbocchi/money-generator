package setup

// The checks from tests/test_config_paths.sh. They read the repo's own
// user_data/config.json and strategy files, read-only, and skip when the checkout
// lacks them.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const repoDir = "../.."

// stripLineComments removes // comments outside strings. freqtrade reads config.json
// with rapidjson in comment mode. test_config_paths.sh used sed 's://.*::', which
// would also cut a "//" inside a string such as a URL.
func stripLineComments(b []byte) []byte {
	var out bytes.Buffer
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		ch := b[i]
		switch {
		case inStr:
			out.WriteByte(ch)
			if esc {
				esc = false
			} else if ch == '\\' {
				esc = true
			} else if ch == '"' {
				inStr = false
			}
		case ch == '"':
			inStr = true
			out.WriteByte(ch)
		case ch == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out.WriteByte('\n')
			}
		default:
			out.WriteByte(ch)
		}
	}
	return out.Bytes()
}

func TestStripLineComments(t *testing.T) {
	in := "{\n  // a comment\n  \"url\": \"https://x\", // trailing\n  \"q\": \"a\\\"//b\"\n}"
	var v map[string]string
	if err := json.Unmarshal(stripLineComments([]byte(in)), &v); err != nil {
		t.Fatal(err)
	}
	if v["url"] != "https://x" || v["q"] != `a"//b` {
		t.Fatalf("got %q", v)
	}
}

func liveConfig(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoDir, "user_data/config.json"))
	if err != nil {
		t.Skipf("no user_data/config.json in this checkout: %v", err)
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(stripLineComments(b), &cfg); err != nil {
		t.Fatalf("config.json does not parse: %v", err)
	}
	return cfg
}

func TestConfigIncludesResolve(t *testing.T) {
	cfg := liveConfig(t)
	var includes []string
	if err := json.Unmarshal(cfg["add_config_files"], &includes); err != nil || len(includes) == 0 {
		t.Fatalf("config.json lists no add_config_files: %v", err)
	}
	for _, inc := range includes {
		// paths are relative to the directory holding config.json
		if fi, err := os.Stat(filepath.Join(repoDir, "user_data", inc)); err != nil || !fi.Mode().IsRegular() {
			t.Errorf("include MISSING: %s", inc)
		}
	}
}

func TestConfigStrategyIsNFIX7Risk(t *testing.T) {
	var s string
	_ = json.Unmarshal(liveConfig(t)["strategy"], &s)
	if s != "NFIX7Risk" {
		t.Fatalf("strategy is %q, want NFIX7Risk", s)
	}
}

// Leverage stays at 2.0 on all three keys. The value must be written 2.0, as the
// bash compared Python's str() of it.
func TestConfigLeverageIs2(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader(liveConfig(t)["nfi_parameters"]))
	dec.UseNumber()
	var p map[string]any
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("nfi_parameters: %v", err)
	}
	for _, k := range []string{"futures_mode_leverage", "futures_mode_leverage_rebuy_mode", "futures_mode_leverage_grind_mode"} {
		if n, ok := p[k].(json.Number); !ok || n.String() != "2.0" {
			t.Errorf("%s is %v, want 2.0", k, p[k])
		}
	}
}

func TestStrategySymlink(t *testing.T) {
	link := filepath.Join(repoDir, "user_data/strategies/NostalgiaForInfinityX7.py")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Skipf("no strategy symlink in this checkout: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", link)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != "../../NostalgiaForInfinity/NostalgiaForInfinityX7.py" {
		t.Errorf("symlink target is %q, want ../../NostalgiaForInfinity/NostalgiaForInfinityX7.py", target)
	}
	if _, err := os.Stat(link); err != nil {
		t.Errorf("symlink dangling: %v", err)
	}
}

func TestNFIX7RiskSource(t *testing.T) {
	path := filepath.Join(repoDir, "user_data/strategies/NFIX7Risk.py")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no NFIX7Risk.py in this checkout: %v", err)
	}
	if !strings.Contains(string(b), "def protections") {
		t.Error("NFIX7Risk lost its protections property")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed; syntax not checked")
	}
	out, err := exec.Command(py, "-I", "-c", "import ast,sys; ast.parse(open(sys.argv[1]).read())", path).CombinedOutput()
	if err != nil {
		t.Errorf("NFIX7Risk.py has a syntax error: %v\n%s", err, out)
	}
}
