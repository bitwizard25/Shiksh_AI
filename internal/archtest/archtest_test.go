// Package archtest enforces the Clean Architecture dependency rule: source dependencies point inward.
package archtest

import (
	"bufio"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// moduleRoot walks up from the test's working directory to the directory holding go.mod
// and returns it with the module path.
func moduleRoot(t *testing.T) (dir, module string) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		f, err := os.Open(filepath.Join(dir, "go.mod"))
		if err == nil {
			defer f.Close()
			s := bufio.NewScanner(f)
			for s.Scan() {
				if m, ok := strings.CutPrefix(strings.TrimSpace(s.Text()), "module "); ok {
					return dir, strings.TrimSpace(m)
				}
			}
			t.Fatal("go.mod has no module line")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestDependencyRule(t *testing.T) {
	root, module := moduleRoot(t)
	internal := module + "/internal/"
	rules := []struct {
		dir       string
		forbidden []string
	}{
		{"internal/entity", []string{internal}},
		{"internal/usecase", []string{internal + "adapter", internal + "infrastructure", internal + "bootstrap", internal + "archtest"}},
		{"internal/adapter", []string{internal + "bootstrap"}},
		{"internal/infrastructure", []string{internal + "adapter", internal + "bootstrap"}},
	}
	for _, rule := range rules {
		base := filepath.Join(root, filepath.FromSlash(rule.dir))
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				for _, bad := range rule.forbidden {
					if strings.HasPrefix(p, bad) {
						rel, _ := filepath.Rel(root, path)
						t.Errorf("%s imports %s: %s must not depend on %s", filepath.ToSlash(rel), p, rule.dir, strings.TrimPrefix(bad, module+"/"))
					}
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("walk %s: %v", rule.dir, err)
		}
	}
}

// TestLayerImportAllowlist is the mirror image of TestDependencyRule: instead of a denylist of
// forbidden prefixes, it enforces an explicit allowlist of the only imports each innermost layer
// may use. This catches anything TestDependencyRule's prefix checks might miss (e.g. a
// third-party package unrelated to any of our own layers).
func TestLayerImportAllowlist(t *testing.T) {
	root, module := moduleRoot(t)
	entityPkg := module + "/internal/entity"
	usecasePkg := module + "/internal/usecase"
	rules := []struct {
		dir     string
		allowed func(imp string) bool
	}{
		{
			dir: "internal/entity",
			allowed: func(imp string) bool {
				return isStdlibImport(imp) || imp == "github.com/google/uuid"
			},
		},
		{
			dir: "internal/usecase",
			allowed: func(imp string) bool {
				if isStdlibImport(imp) || imp == "github.com/google/uuid" || imp == entityPkg {
					return true
				}
				return imp == usecasePkg || strings.HasPrefix(imp, usecasePkg+"/")
			},
		},
	}
	for _, rule := range rules {
		base := filepath.Join(root, filepath.FromSlash(rule.dir))
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if !rule.allowed(p) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s imports %s: not on the %s allowlist", filepath.ToSlash(rel), p, rule.dir)
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("walk %s: %v", rule.dir, err)
		}
	}
}

// isStdlibImport reports whether imp is a standard-library import: its first path element has no
// dot. Third-party and module-local imports always have a dot (a domain) or are the module path.
func isStdlibImport(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}
