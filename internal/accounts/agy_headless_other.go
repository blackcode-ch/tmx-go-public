//go:build !windows

package accounts

import (
	"context"
	"errors"
)

func (s *scope) runHeadlessAgyQuota(ctx context.Context, path string) ran {
	return s.run(ctx, s.o.CheckTimeout, path, "-p", "/usage", "--output-format", "json")
}

func RunAgyQuotaChild() error {
	return errors.New("Antigravity headless quota helper is Windows-only")
}
