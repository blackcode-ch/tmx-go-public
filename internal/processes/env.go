package processes

import "strings"

func toolEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, v := range env {
		k, _, _ := strings.Cut(v, "=")
		k = strings.ToUpper(k)
		if strings.HasPrefix(k, "TMX") || strings.HasPrefix(k, "CLAUDE_CODE") || k == "CLAUDECODE" {
			continue
		}
		out = append(out, v)
	}
	return out
}
