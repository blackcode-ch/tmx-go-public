package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/rollback"
)

func installationPath(exe string) string {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if i := strings.Index(exe, ".app"+string(filepath.Separator)+"Contents"+string(filepath.Separator)+"MacOS"+string(filepath.Separator)); i >= 0 {
		return exe[:i+len(".app")]
	}
	return exe
}

func cmdRollback(e *env, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: tmx rollback (stop the daemon first)")
	}
	if err := e.load(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return rollbackInstallation(e, exe)
}

func rollbackInstallation(e *env, exe string) error {
	lock, err := config.AcquireLock(e.paths)
	if err != nil {
		return fmt.Errorf("rollback requires a stopped daemon: %w", err)
	}
	defer lock.Release()
	unproven, err := rollback.Unproven(filepath.Join(e.paths.Home, "rollback"), installationPath(exe), e.paths.DB())
	if err != nil {
		return err
	}
	if err := rollback.Restore(filepath.Join(e.paths.Home, "rollback"), installationPath(exe), e.paths.DB()); err != nil {
		return err
	}
	if unproven {
		fmt.Fprintln(e.out, "Database restored. No proven previous binary is available; install a compatible binary before restarting.")
		return nil
	}
	fmt.Fprintln(e.out, "Previous binary and database restored. Start tmx again.")
	return nil
}
