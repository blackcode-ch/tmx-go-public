//go:build darwin

package antechamber

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// markFromElsewhere sets the quarantine attribute (Gatekeeper/XProtect check on open). Best effort.
func markFromElsewhere(path, _ string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	val := fmt.Sprintf("0081;%x;tmx;", time.Now().Unix())
	_ = hiddenexec.CommandContext(ctx, "xattr", "-w", "com.apple.quarantine", val, path).Run()
}

// noExec removes execute bits.
func noExec(path string) { _ = os.Chmod(path, 0o644) }
