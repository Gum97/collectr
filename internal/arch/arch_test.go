// Package arch holds the test that keeps the modular monolith modular.
//
// Module boundaries that live only in a document erode within weeks: someone
// needs one field from another module, reaches into its store, and nobody
// notices until extracting a service means untangling a year of shortcuts. This
// test is the enforcement.
package arch

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/collectr/collectr"

// isCompositionRoot reports whether pkg is one of the cmd/ entrypoints.
//
// The composition root is the one place allowed to see every module: wiring
// concrete stores into services is exactly its job. Every other package is held
// to the boundaries.
func isCompositionRoot(pkg string) bool {
	return strings.HasPrefix(pkg, modulePath+"/cmd/")
}

// TestModuleBoundaries verifies the import rules documented in
// docs/05-architecture.md.
func TestModuleBoundaries(t *testing.T) {
	imports := packageImports(t)

	tests := []struct {
		name   string
		verify func(t *testing.T, pkg string, deps []string)
	}{
		{
			name: "a module's store is private to that module",
			verify: func(t *testing.T, pkg string, deps []string) {
				if isCompositionRoot(pkg) {
					return
				}
				owner, ok := moduleOf(pkg)
				for _, dep := range deps {
					depOwner, isModule := moduleOf(dep)
					if !isModule || !strings.HasSuffix(dep, "/store") {
						continue
					}
					if !ok || owner != depOwner {
						t.Errorf("%s imports %s: a module's store is private to that module", pkg, dep)
					}
				}
			},
		},
		{
			name: "modules see each other only through contracts",
			verify: func(t *testing.T, pkg string, deps []string) {
				owner, ok := moduleOf(pkg)
				if !ok {
					return
				}
				for _, dep := range deps {
					depOwner, isModule := moduleOf(dep)
					if !isModule || depOwner == owner {
						continue
					}
					t.Errorf("%s imports %s: cross-module use must go through %s/internal/contracts",
						pkg, dep, modulePath)
				}
			},
		},
		{
			name: "consent and audit depend on no business module",
			verify: func(t *testing.T, pkg string, deps []string) {
				owner, ok := moduleOf(pkg)
				if !ok || (owner != "consent" && owner != "audit" && owner != "dsr") {
					return
				}
				for _, dep := range deps {
					if depOwner, isModule := moduleOf(dep); isModule && depOwner != owner {
						t.Errorf("%s imports %s: the compliance modules must stay a one-way dependency",
							pkg, dep)
					}
				}
			},
		},
		{
			name: "platform never depends on a module",
			verify: func(t *testing.T, pkg string, deps []string) {
				if !strings.HasPrefix(pkg, modulePath+"/internal/platform/") {
					return
				}
				for _, dep := range deps {
					if strings.HasPrefix(dep, modulePath+"/internal/modules/") {
						t.Errorf("%s imports %s: platform is the base layer and must not know about modules",
							pkg, dep)
					}
				}
			},
		},
		{
			name: "contracts stays dependency-free",
			verify: func(t *testing.T, pkg string, deps []string) {
				if pkg != modulePath+"/internal/contracts" {
					return
				}
				for _, dep := range deps {
					if strings.HasPrefix(dep, modulePath+"/internal/") {
						t.Errorf("contracts imports %s: it must stay importable from anywhere", dep)
					}
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for pkg, deps := range imports {
				tc.verify(t, pkg, deps)
			}
		})
	}
}

// moduleOf returns the module a package belongs to, if any.
func moduleOf(pkg string) (string, bool) {
	const prefix = modulePath + "/internal/modules/"
	if !strings.HasPrefix(pkg, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(pkg, prefix)
	name, _, _ := strings.Cut(rest, "/")
	return name, name != ""
}

// packageImports maps each of the project's packages to its project-internal
// imports, as reported by the Go toolchain itself.
func packageImports(t *testing.T) map[string][]string {
	t.Helper()

	out, err := exec.Command("go", "list", "-deps=false",
		"-f", "{{.ImportPath}} {{join .Imports \",\"}}|{{join .TestImports \",\"}}",
		modulePath+"/...").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			t.Fatalf("go list: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatalf("go list: %v", err)
	}

	result := make(map[string][]string)
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		head, testImports, _ := strings.Cut(line, "|")
		pkg, imports, _ := strings.Cut(head, " ")
		if pkg == "" {
			continue
		}
		var deps []string
		for _, group := range []string{imports, testImports} {
			for _, dep := range strings.Split(group, ",") {
				if strings.HasPrefix(dep, modulePath+"/") {
					deps = append(deps, dep)
				}
			}
		}
		result[pkg] = deps
	}
	if len(result) == 0 {
		t.Fatal("go list returned no packages")
	}
	return result
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
