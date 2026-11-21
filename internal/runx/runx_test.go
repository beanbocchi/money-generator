package runx

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExec(t *testing.T) {
	ctx := context.Background()
	sh := func(e Exec, script string) (string, error) { return e.Run(ctx, "sh", "-c", script) }

	out, err := sh(Exec{}, "echo out; echo err >&2")
	if err != nil || out != "out\n" {
		t.Fatalf("default: out=%q err=%v", out, err)
	}
	out, err = sh(Exec{Combined: true}, "echo out; echo err >&2")
	if err != nil || !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Fatalf("combined: out=%q err=%v", out, err)
	}
	var so, se strings.Builder
	if _, err := sh(Exec{Stdout: &so, Stderr: &se}, "echo a; echo b >&2"); err != nil || so.String() != "a\n" || se.String() != "b\n" {
		t.Fatalf("stream: %q %q %v", so.String(), se.String(), err)
	}
	var both strings.Builder
	out, err = sh(Exec{Combined: true, Stdout: &both}, "echo a; echo b >&2")
	if err != nil || out != "a\nb\n" || both.String() != out {
		t.Fatalf("combined stream: out=%q streamed=%q err=%v", out, both.String(), err)
	}
	_, err = sh(Exec{}, "echo boom >&2; exit 3")
	if ExitCode(err) != 3 || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failure: code=%d err=%v", ExitCode(err), err)
	}
	if ExitCode(nil) != 0 {
		t.Fatal("ExitCode(nil) != 0")
	}
}

func TestExecGraceSendsSIGTERM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	// The trap proves SIGTERM arrived: exec's default SIGKILL cannot be trapped.
	out, _ := Exec{Grace: 5 * time.Second}.Run(ctx, "sh", "-c", `trap "echo term; exit 0" TERM; sleep 10 >/dev/null 2>&1 & wait`)
	if !strings.Contains(out, "term") {
		t.Fatalf("child did not see SIGTERM: %q", out)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("waited for the grace period although the child exited")
	}
}
