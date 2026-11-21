// Command mg is the single entry point for the bot's sidecars and host tools.
//
//	mg <command> [flags]
//
// Each command lives in its own package under internal/ and exposes
// Main(ctx, args) int, so it can be tested without a process boundary.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"syscall"
)

type command struct {
	summary string
	run     func(ctx context.Context, args []string) int
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage()
		return 2
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "mg: unknown command %q\n\n", args[0])
		usage()
		return 2
	}
	// One logger for every command: text to stderr, which `docker logs` shows.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cmd.run(ctx, args[1:])
}

func usage() {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "usage: mg <command> [flags]\n\ncommands:")
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %-16s %s\n", n, commands[n].summary)
	}
}
