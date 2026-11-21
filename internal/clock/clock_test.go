package clock

import (
	"context"
	"testing"
	"time"
)

func TestSleep(t *testing.T) {
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Hour); err == nil {
		t.Fatal("cancelled sleep returned nil")
	}
}
