// Package testpolicy checks test-source safety without running the tested code.
package testpolicy

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These calls either change a DACL explicitly or exercise config's low-level
// private creation primitives. Higher-level config consumers are not a call
// graph: their new-file security descriptors remain normal production behavior.
var aclCalls = map[string]bool{
	"setDACL": true, "SetDACL": true, "SetNamedSecurityInfo": true,
	"SetSecurityInfo": true, "SetFileSecurity": true, "SetSecurityDescriptorDacl": true,
	"CreateDirectory": true, "secretSD": true, "securityAttributes": true,
	"MkdirSecret": true, "mkdirSecret": true, "createSecret": true,
	"CreateSecretFile": true, "WriteSecretFile": true,
}

func needsACLTag(source string) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, 0)
	if err != nil {
		return false, err
	}
	mutates := false
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && aclCalls[id.Name] {
			mutates = true
		}
		return true
	})
	return mutates, nil
}

// Require that the expression cannot select Windows without aclmutate,
// including when arbitrary other tags are supplied (aclmutate || other is unsafe).
func excludesPlainWindows(source string) bool {
	for _, line := range strings.Split(source, "\n") {
		if !strings.HasPrefix(line, "//go:build ") {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false
		}
		var tags []string
		seen := map[string]bool{"windows": true, "aclmutate": true}
		expr.Eval(func(tag string) bool {
			if !seen[tag] {
				tags = append(tags, tag)
				seen[tag] = true
			}
			return false
		})
		if len(tags) > 12 {
			return false
		}
		for mask := 0; mask < 1<<len(tags); mask++ {
			if expr.Eval(func(tag string) bool {
				if tag == "windows" {
					return true
				}
				if tag == "aclmutate" {
					return false
				}
				for i, candidate := range tags {
					if candidate == tag {
						return mask&(1<<i) != 0
					}
				}
				return false
			}) {
				return false
			}
		}
		return true
	}
	return false
}

func TestACLMutationRequiresOptIn(t *testing.T) {
	err := filepath.WalkDir(filepath.Join("..", ".."), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mutates, err := needsACLTag(string(b))
		if err != nil {
			return err
		}
		if mutates && !excludesPlainWindows(string(b)) {
			t.Errorf("%s: DACL test requires //go:build aclmutate (or !windows || aclmutate)", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestACLTagGuard(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		safe bool
	}{
		{"", false}, {"aclmutate", true}, {"!windows || aclmutate", true},
		{"windows && aclmutate", true}, {"aclmutate || bypass", false},
		{"!aclmutate", false}, {"windows", false},
	} {
		source := "package fixture\nfunc f() { windows.SetNamedSecurityInfo() }"
		if tc.tag != "" {
			source = "//go:build " + tc.tag + "\n\n" + source
		}
		mutates, err := needsACLTag(source)
		if err != nil || !mutates {
			t.Fatalf("mutation not detected: %v", err)
		}
		if got := excludesPlainWindows(source); got != tc.safe {
			t.Errorf("tag %q: safe=%v, want %v", tc.tag, got, tc.safe)
		}
	}
	mutates, err := needsACLTag("package fixture\n// windows.SetNamedSecurityInfo()\nvar note = \"setDACL\"")
	if err != nil || mutates {
		t.Fatalf("comment/string treated as call: %v", err)
	}
}
