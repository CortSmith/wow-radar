package core

import (
	"context"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	modulePath = "radarlive"
	modPrefix  = modulePath + "/internal/mod/"
	// sharedTestHelper is the one module package other modules' tests may import.
	sharedTestHelper = "storetest"
)

// importsOf returns the import paths of a Go file.
func importsOf(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var paths []string
	for _, spec := range file.Imports {
		paths = append(paths, strings.Trim(spec.Path.Value, `"`))
	}
	return paths
}

// goFiles lists the .go files directly or indirectly under dir.
func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return files
}

// violations checks the modularity rules for the module tree rooted at root and returns one line per breach.
func violations(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	found = append(found, modImportsOfModules(t, root)...)
	found = append(found, foundationImports(t, root)...)
	found = append(found, cmdImports(t, root)...)
	return found
}

// modImportsOfModules: a module package may import only core and domain (plus storetest from tests).
func modImportsOfModules(t *testing.T, root string) []string {
	var found []string
	for _, file := range goFiles(t, filepath.Join(root, "internal", "mod")) {
		relPath, _ := filepath.Rel(filepath.Join(root, "internal", "mod"), file)
		rel := filepath.ToSlash(relPath)
		self := strings.Split(rel, "/")[0]
		isTest := strings.HasSuffix(file, "_test.go")
		for _, imp := range importsOf(t, file) {
			if !strings.HasPrefix(imp, modPrefix) {
				continue
			}
			other := strings.Split(strings.TrimPrefix(imp, modPrefix), "/")[0]
			if other == self || (isTest && other == sharedTestHelper) {
				continue
			}
			found = append(found, rel+" imports module "+other)
		}
	}
	return found
}

// foundationImports: core and domain must not depend on any module; domain depends on nothing of ours.
func foundationImports(t *testing.T, root string) []string {
	var found []string
	for _, pkg := range []string{"core", "domain"} {
		for _, file := range goFiles(t, filepath.Join(root, "internal", pkg)) {
			for _, imp := range importsOf(t, file) {
				if strings.HasPrefix(imp, modPrefix) {
					found = append(found, pkg+" imports module "+imp)
				}
				if pkg == "domain" && strings.HasPrefix(imp, modulePath+"/") {
					found = append(found, "domain imports "+imp)
				}
			}
		}
	}
	return found
}

// cmdImports: modules.go is the only file under cmd that may import module packages.
func cmdImports(t *testing.T, root string) []string {
	var found []string
	for _, file := range goFiles(t, filepath.Join(root, "cmd")) {
		if filepath.Base(file) == "modules.go" {
			continue
		}
		for _, imp := range importsOf(t, file) {
			if strings.HasPrefix(imp, modPrefix) {
				found = append(found, filepath.Base(file)+" imports module "+imp+" (only modules.go may)")
			}
		}
	}
	return found
}

func TestModulesStayIndependent(t *testing.T) {
	if got := violations(t, filepath.Join("..", "..")); len(got) > 0 {
		t.Fatalf("architecture violations:\n%s", strings.Join(got, "\n"))
	}
}

func TestViolationDetectorFindsBreaches(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/mod/a/a.go", `package a; import _ "radarlive/internal/mod/b"`)
	write("internal/mod/a/a_test.go", `package a; import _ "radarlive/internal/mod/storetest"`)
	write("internal/mod/b/b.go", `package b; import _ "radarlive/internal/mod/b/sub"`)
	write("internal/core/c.go", `package core; import _ "radarlive/internal/mod/a"`)
	write("internal/domain/d.go", `package domain; import _ "radarlive/internal/core"`)
	write("cmd/x/main.go", `package main; import _ "radarlive/internal/mod/a"`)
	write("cmd/x/modules.go", `package main; import _ "radarlive/internal/mod/a"`)

	got := violations(t, root)
	want := []string{
		"a/a.go imports module b",
		"core imports module radarlive/internal/mod/a",
		"domain imports radarlive/internal/core",
		"main.go imports module radarlive/internal/mod/a (only modules.go may)",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("violations =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestHostWorksWithZeroModules is the removal test: the core alone starts, serves /healthz and stops.
func TestHostWorksWithZeroModules(t *testing.T) {
	log, _ := bufferLogger()
	h := NewHost(WithLogger(log))
	if err := h.Config.Load([]string{"--listen", "127.0.0.1:0"}, envOf(nil)); err != nil {
		t.Fatal(err)
	}
	if len(h.ModuleNames()) != 0 {
		t.Fatal("a bare host has no modules")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	select {
	case <-h.Ready():
	case err := <-done:
		t.Fatalf("host exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("host never became ready")
	}
	resp, err := http.Get("http://" + h.Addr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("healthz = %d %q", resp.StatusCode, body)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
