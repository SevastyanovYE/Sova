package telegrammt

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/SevastyanovYE/Sova/internal/config"
)

func TestSessionLockSerializesCallers(t *testing.T) {
	client := New(config.Config{TelegramSessionPath: filepath.Join(t.TempDir(), "session.json")})
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- client.withSessionLock(context.Background(), func() error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- client.withSessionLock(context.Background(), func() error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second caller entered while first held the session lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("second caller did not acquire the released session lock")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}
