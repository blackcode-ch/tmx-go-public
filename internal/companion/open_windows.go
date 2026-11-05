package companion

import (
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

var shellOpenURL = hiddenexec.OpenURL

// Use ShellExecute directly: no command interpreter and no console window.
func openWindowsURL(url string) error {
	if url != DeepLink {
		return fmt.Errorf("only Companion's connect link can be opened")
	}
	if err := shellOpenURL(url); err != nil {
		return fmt.Errorf("%s: %w", errNotOpened, err)
	}
	return nil
}
