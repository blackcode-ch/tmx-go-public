package hiddenexec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Parse every platform and tests too, so the host's build tags cannot hide a bypass.
func TestProcessConstructorsUseHelper(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if filepath.ToSlash(rel) == "internal/hiddenexec/command.go" {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, pos := range bypasses(f) {
			t.Errorf("%s: use hiddenexec for process creation", fset.Position(pos))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func bypasses(f *ast.File) []token.Pos {
	imports := map[string]string{}
	var bad []token.Pos
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imports[name] = path
		if name == "." && (path == "os/exec" || path == "os" || path == "syscall" || path == "golang.org/x/sys/windows") {
			bad = append(bad, imp.Pos())
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.CompositeLit); ok {
			if typ, ok := lit.Type.(*ast.SelectorExpr); ok {
				if id, ok := typ.X.(*ast.Ident); ok && imports[id.Name] == "os/exec" && typ.Sel.Name == "Cmd" {
					bad = append(bad, lit.Pos())
				}
			}
		}
		s, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := s.X.(*ast.Ident)
		if !ok {
			return true
		}
		path, name := imports[id.Name], s.Sel.Name
		if (path == "os/exec" && (name == "Command" || name == "CommandContext")) ||
			(path == "os" && name == "StartProcess") ||
			((path == "syscall" || path == "golang.org/x/sys/windows") &&
				(strings.HasPrefix(name, "CreateProcess") || strings.HasPrefix(name, "ShellExecute") || name == "StartProcess" || name == "ForkExec" || name == "Exec")) {
			bad = append(bad, s.Pos())
		}
		return true
	})
	return bad
}

// restrictedConstructors confines the narrow-purpose hiddenexec constructors to the one
// call site each was added for, so a future caller cannot reach for the exception instead
// of asking whether their child really needs a visible console or window.
var restrictedConstructors = map[string]string{
	"CommandInheritConsole": "internal/cli/slot.go",
	"CommandGUI":            "internal/desktop/app.go",
}

func TestRestrictedConstructorsStayAtTheirCallSite(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel := filepath.ToSlash(func() string { r, _ := filepath.Rel(root, path); return r }())
		if rel == "internal/hiddenexec/command.go" || strings.HasPrefix(rel, "internal/hiddenexec/") {
			return nil // the constructors themselves, and their own tests
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		imports := map[string]string{}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			name := filepath.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = p
		}
		ast.Inspect(f, func(n ast.Node) bool {
			s, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := s.X.(*ast.Ident)
			if !ok {
				return true
			}
			if imports[id.Name] != "github.com/blackcode-ch/tmx-go/internal/hiddenexec" {
				return true
			}
			if site, restricted := restrictedConstructors[s.Sel.Name]; restricted && rel != site {
				t.Errorf("%s: hiddenexec.%s is reserved for %s", fset.Position(s.Pos()), s.Sel.Name, site)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLintDetectsAliasesAndFunctionReferences(t *testing.T) {
	for _, src := range []string{
		`package p; import "os/exec"; var x = exec.Command("tool")`,
		`package p; import e "os/exec"; var x = e.CommandContext`,
		`package p; import . "os/exec"; var x = Command`,
		`package p; import o "os"; var x = o.StartProcess`,
		`package p; import w "golang.org/x/sys/windows"; var x = w.ShellExecute`,
		`package p; import e "os/exec"; var x = &e.Cmd{Path: "tool"}`,
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(bypasses(f)) == 0 {
			t.Errorf("missed bypass: %s", src)
		}
	}
}
