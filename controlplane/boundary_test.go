package controlplane_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestControlPlaneDoesNotImportWorkload makes the central architectural claim
// of this project machine-checked instead of aspirational.
//
// The claim is that the control plane is general: that a different LLM-backed
// application could be dropped in without changing it. That claim is only
// worth stating if nothing in controlplane/ has quietly reached into
// workload/, and the cheapest way for it to stop being true is for somebody to
// import one useful helper across the line at 2am.
func TestControlPlaneDoesNotImportWorkload(t *testing.T) {
	const forbidden = "multitenant-saas-on-kubernetes/workload"

	root, err := os.Getwd() // the controlplane/ directory
	if err != nil {
		t.Fatal(err)
	}

	var checked int
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++

		for _, imp := range file.Imports {
			if strings.Contains(imp.Path.Value, forbidden) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s imports %s\n"+
					"  the control plane must not depend on the demonstration application:\n"+
					"  that dependency is what would make it specific to one app rather than general.",
					rel, imp.Path.Value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A walk that silently found nothing would pass forever while checking
	// nothing, which is worse than no test at all.
	if checked == 0 {
		t.Fatal("found no Go files under controlplane/; the test is not actually checking anything")
	}
	t.Logf("boundary verified across %d files", checked)
}
