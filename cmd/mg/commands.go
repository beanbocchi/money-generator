package main

import (
	"github.com/beanbocchi/money-generator/internal/dbbackup"
	"github.com/beanbocchi/money-generator/internal/lab"
	"github.com/beanbocchi/money-generator/internal/newsgate"
	"github.com/beanbocchi/money-generator/internal/setup"
	"github.com/beanbocchi/money-generator/internal/status"
	"github.com/beanbocchi/money-generator/internal/updater"
	"github.com/beanbocchi/money-generator/internal/watchdog"
)

// commands is the whole CLI. Sidecars run `mg updater|watchdog|dbbackup|newsgate`;
// the rest are host tools.
var commands = map[string]command{
	"updater":        {"track upstream NFI and restart the bot behind the fuse", updater.Main},
	"approve-update": {"accept an update the fuse held", updater.Approve},
	"rollback":       {"put the bot back on the previous NFI commit", updater.Rollback},
	"watchdog":       {"alert when the bot stops responding or trading", watchdog.Main},
	"dbbackup":       {"snapshot the trade database every interval", dbbackup.Main},
	"status":         {"health and account report", status.Main},
	"setup":          {"prepare a new machine", setup.Main},
	"newsgate":       {"shadow LLM news-event gate (ADR-0027)", newsgate.Main},
	"backtest":       {"run one lab backtest", lab.Backtest},
	"download":       {"download lab candle data", lab.Download},
	"queue":          {"run a lab matrix serially", lab.Queue},
	"tsmom":          {"ADR-0026 trend baseline", lab.TSMOM},
}
