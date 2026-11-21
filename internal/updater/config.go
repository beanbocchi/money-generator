package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/beanbocchi/money-generator/internal/envfile"
)

// Defaults are the values update.sh used. Every variable is read the way the shell read
// it with ${VAR:-default}: set-but-empty means "use the default".
const (
	defRiskPattern = "stop|doom|lever|derisk|grind|rebuy|buyback|stake|position_adjustment|max_entry|trailing|roi|system_"
	defVolURL      = "https://fapi.binance.com/fapi/v1/klines?symbol=BTCUSDT&interval=5m&limit=6"
	defVolLongURL  = "https://fapi.binance.com/fapi/v1/klines?symbol=BTCUSDT&interval=1h&limit=4"
	defUnhealthy   = "-2015|Invalid API-key|AuthenticationError|Could not load markets"
)

// HostStateDir is the updater's state directory relative to the repo root, where the
// host tools (Approve, Rollback) find it. The container mounts it as /state.
const HostStateDir = "services/updater/state"

// Config is everything the updater reads from its environment.
type Config struct {
	NFIDir         string
	StateDir       stateDir
	Docker         string // docker binary, a name on PATH or a path
	Target         string // the bot container
	StrategyModule string
	StrategyFile   string
	Interval       time.Duration
	DryRun         bool
	RunOnce        bool
	WatchPaths     []string // pathspecs passed to git diff

	// The fuse (ADR-0010). A zero gap or limit disables that check.
	RiskPattern       *regexp.Regexp
	MaxRestartsPerDay int
	MinRestartGap     time.Duration
	VolLimit          float64 // percent, 30m window
	VolURL            string
	VolLongLimit      float64 // percent, 4h window
	VolLongURL        string
	Unhealthy         *regexp.Regexp // matched against the whole output, (?m)
	UnhealthyWindow   string

	// Telegram: freqtrade's bot for plain pages, a separate bot for buttons.
	TelegramAPI   string // "" means telegram.Client's default
	NotifyToken   string
	NotifyChat    string
	ApprovalToken string
	ApprovalChat  string
	Approvers     []string
	Selftest      bool
	SelftestWait  time.Duration
}

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

// LoadConfig builds a Config from the environment. Unlike the shell, which compared
// garbage numbers as strings and carried on, a value that does not parse is an error,
// so a typo in .env stops the updater instead of quietly disabling a check.
func LoadConfig() (Config, error) {
	or := envfile.Or
	c := Config{
		NFIDir:          or("NFI_DIR", "/nfi"),
		StateDir:        stateDir(or("STATE_DIR", "/state")),
		Docker:          or("DOCKER", "docker"),
		Target:          or("TARGET_CONTAINER", "bottrade"),
		StrategyModule:  or("STRATEGY_MODULE", "NFIX7Risk"),
		StrategyFile:    or("STRATEGY_FILE", "NostalgiaForInfinityX7.py"),
		DryRun:          or("DRY_RUN", "0") == "1",
		RunOnce:         or("RUN_ONCE", "0") == "1",
		WatchPaths:      strings.Fields(or("WATCH_PATHS", "NostalgiaForInfinityX7.py configs/")),
		VolURL:          or("VOL_URL", defVolURL),
		VolLongURL:      or("VOL_LONG_URL", defVolLongURL),
		UnhealthyWindow: or("UNHEALTHY_WINDOW", "15m"),
		TelegramAPI:     or("TELEGRAM_API", ""),
		NotifyToken:     or("FREQTRADE__TELEGRAM__TOKEN", ""),
		NotifyChat:      or("FREQTRADE__TELEGRAM__CHAT_ID", ""),
		ApprovalToken:   or("APPROVAL_TELEGRAM_TOKEN", ""),
		Selftest:        or("APPROVAL_SELFTEST", "0") == "1",
	}
	var err error
	if c.Interval, err = envfile.Seconds("UPDATE_INTERVAL", 900); err != nil {
		return Config{}, err
	}
	if c.MaxRestartsPerDay, err = envfile.Int("MAX_RESTARTS_PER_DAY", 4); err != nil {
		return Config{}, err
	}
	if c.MinRestartGap, err = envfile.Seconds("MIN_RESTART_GAP", 21600); err != nil {
		return Config{}, err
	}
	if c.SelftestWait, err = envfile.Seconds("SELFTEST_SECONDS", 180); err != nil {
		return Config{}, err
	}
	if c.VolLimit, err = percent("VOL_LIMIT_PCT", "2"); err != nil {
		return Config{}, err
	}
	if c.VolLongLimit, err = percent("VOL_LONG_LIMIT_PCT", "4"); err != nil {
		return Config{}, err
	}
	if c.RiskPattern, err = compile("RISK_PATTERN", or("RISK_PATTERN", defRiskPattern), ""); err != nil {
		return Config{}, err
	}
	// grep matched line by line; (?m) keeps ^ and $ at line ends on the whole output.
	if c.Unhealthy, err = compile("UNHEALTHY_PATTERN", or("UNHEALTHY_PATTERN", defUnhealthy), "(?m)"); err != nil {
		return Config{}, err
	}

	// approval.sh: the button chat defaults to freqtrade's chat; approvers default to the
	// chat id only when that is a private chat (a positive number). A group needs
	// APPROVER_IDS, or the buttons stay off.
	c.ApprovalChat = or("APPROVAL_CHAT_ID", c.NotifyChat)
	ids := or("APPROVER_IDS", "")
	if ids == "" && digitsOnly.MatchString(c.ApprovalChat) {
		ids = c.ApprovalChat
	}
	// Split on spaces only: the shell matched " id " in the padded list.
	for _, id := range strings.Split(strings.ReplaceAll(ids, ",", " "), " ") {
		if id != "" {
			c.Approvers = append(c.Approvers, id)
		}
	}
	return c, nil
}

// percent reads a volatility limit. The shell disabled the check only for the exact
// string "0" and compared anything else, so "0.0" meant "never restart". Here 0
// disables, and any other spelling of zero is refused rather than turning the gate off.
func percent(key, def string) (float64, error) {
	v := envfile.Or(key, def)
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a number: %w", key, v, err)
	}
	if f == 0 && v != "0" {
		return 0, fmt.Errorf("%s=%q: write 0 to disable the check", key, v)
	}
	return f, nil
}

func compile(key, pat, flags string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(flags + pat)
	if err != nil {
		return nil, fmt.Errorf("%s=%q is not a valid regular expression: %w", key, pat, err)
	}
	return re, nil
}

// ApprovalEnabled is approval.sh's approval_enabled: a token, a chat and at least one
// approver.
func (c Config) ApprovalEnabled() bool {
	return c.ApprovalToken != "" && c.ApprovalChat != "" && len(c.Approvers) > 0
}

func (c Config) isApprover(id string) bool { return slices.Contains(c.Approvers, id) }

// stateDir holds the updater's state files, each one line written by `echo v > file`.
type stateDir string

func (d stateDir) Path(name string) string { return filepath.Join(string(d), name) }

// Read is `$(cat file 2>/dev/null)`: the content without trailing newlines, or "" when
// the file cannot be read.
func (d stateDir) Read(name string) string {
	b, err := os.ReadFile(d.Path(name))
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\n")
}

// Write is `echo "$value" > file`.
func (d stateDir) Write(name, value string) error {
	if err := os.WriteFile(d.Path(name), []byte(value+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// hostDir is the environment variable key (default def) for a host tool. A relative
// path is taken from the repo root, as the bash tools did after cd'ing there.
func hostDir(root, key, def string) string {
	p := envfile.Or(key, def)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}
