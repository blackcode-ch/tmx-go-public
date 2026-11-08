package cli

import "github.com/blackcode-ch/tmx-go/internal/antechamber"

// cmdAntechamber is the hidden `tmx antechamber inspect …`: the windowless child the antechamber
// starts to inspect one quarantined file under a time limit (not listed in the help).
func cmdAntechamber(e *env, args []string) error {
	if code := antechamber.Main(args, e.out, e.errw); code != 0 {
		return exitError{code: code}
	}
	return nil
}
