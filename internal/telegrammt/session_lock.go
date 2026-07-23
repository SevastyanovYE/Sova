package telegrammt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const sessionLockRetry = 250 * time.Millisecond

// withSessionLock serializes access to the on-disk MTProto session across the
// Nest and Workspace processes. A session file is not a safe coordination
// primitive by itself when both services can start a Telegram client.
func (c *Client) withSessionLock(ctx context.Context, run func() error) error {
	path := c.cfg.TelegramSessionPath + ".lock"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open Telegram session lock: %w", err)
	}
	defer file.Close()
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("lock Telegram session: %w", err)
		}
		timer := time.NewTimer(sessionLockRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN) //nolint:errcheck -- closing the descriptor also releases the lock.
	return run()
}
