package cli

import "github.com/blackcode-ch/tmx-go/internal/accounts"

func cmdAgyQuotaChild(_ *env, args []string) error {
	if len(args) != 0 {
		return usageErr("invalid Antigravity quota helper arguments")
	}
	return accounts.RunAgyQuotaChild()
}
