// Package clock has the one time helper every polling loop needs.
package clock

import (
	"context"
	"time"
)

// Sleep waits d or until ctx is done, whichever comes first, and returns ctx.Err() in
// the second case.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
