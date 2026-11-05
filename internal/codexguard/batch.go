package codexguard

import (
	"fmt"
	"strings"
)

// BatchLine accepts only arguments whose value survives cmd's second parse.
// Native targets accept arbitrary literal argv. Batch metacharacters are refused
// with a clear error rather than being guessed at or reinterpreted as commands.
func BatchLine(path string, args []string) (string, error) {
	values := append([]string{path}, args...)
	quoted := make([]string, len(values))
	for i, value := range values {
		if strings.ContainsAny(value, "\x00\r\n\"%!^&|<>") {
			return "", fmt.Errorf("batch wrapper arguments contain shell syntax; invoke the native program directly")
		}
		quoted[i] = `"` + value + `"`
	}
	return `"` + strings.Join(quoted, " ") + `"`, nil
}
