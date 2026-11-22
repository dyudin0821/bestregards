// Package arch holds tests that enforce the dependency rule of the clean architecture:
// inner layers never import outer ones.
package arch

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/dyudin0821/bestregards/"

// allowed lists, per inner package, the only imports it may use besides the standard library.
var allowed = map[string][]string{
	"user":      {},
	"pairs":     {},
	"auth":      {module + "internal/user"},
	"limits":    {module + "internal/user"},
	"events":    {},
	"admin":     {module + "internal/limits", module + "internal/user"},
	"translate": {module + "internal/events", module + "internal/limits", module + "internal/pairs", module + "internal/user"},
}

func TestInnerLayersDoNotImportOuterOnes(t *testing.T) {
	for pkg, ok := range allowed {
		dir := filepath.Join("..", pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if !strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
					continue // standard library
				}
				if !contains(ok, path) {
					t.Errorf("%s/%s imports %s; layer %q may only import %v", pkg, e.Name(), path, pkg, ok)
				}
			}
		}
	}
}

// importers lists packages that only the given directories may import: generated sqlc types must not
// leak out of the store, and only main may run the composition root.
var importers = map[string][]string{
	module + "internal/store/db": {"internal/store"},
	module + "internal/app":      {"cmd/bestregards"},
}

func TestRestrictedPackagesHaveOnlyAllowedImporters(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			dir := filepath.ToSlash(rel)
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if ok, restricted := importers[p]; restricted && !contains(ok, dir) {
					t.Errorf("%s imports %s; only %v may", filepath.ToSlash(path), p, ok)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
