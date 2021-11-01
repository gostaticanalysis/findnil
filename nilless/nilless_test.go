package nilless_test

import (
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gostaticanalysis/findnil/nilless"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/expect"
	"golang.org/x/tools/go/packages"
)

func testdata(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal("unexpected error:", err)
	}
	return filepath.Join(dir, "testdata")
}

func TestLoad(t *testing.T) {
	cfg := &packages.Config{
		Mode: packages.NeedFiles | packages.NeedSyntax | packages.NeedTypesInfo |
			packages.NeedTypes | packages.NeedDeps | packages.NeedModule,
		Dir: filepath.Join(testdata(t), "a"),
	}
	result, err := nilless.Load(cfg, "./...")
	if err != nil {
		t.Fatal("unexpected error:", err)
	}

	var keys []string
	expectNotes := make(map[string]*expect.Note)
	for _, pkg := range result.Pkgs {
		for _, file := range pkg.Syntax {
			notes, err := expect.ExtractGo(pkg.Fset, file)
			if err != nil {
				t.Fatal("unexpected error:", err)
			}
			for _, note := range notes {
				pos := pkg.Fset.Position(note.Pos)
				key := fmt.Sprintf("%s:%s:%d", pkg.Types.Path(), result.Base(pos.Filename), pos.Line)
				keys = append(keys, key)
				expectNotes[key] = note
			}
		}

		inspect := inspector.New(pkg.Syntax)
		inspect.Preorder([]ast.Node{(*ast.Ident)(nil)}, func(n ast.Node) {
			id, _ := n.(*ast.Ident)
			if id == nil {
				return
			}

			if _, ok := pkg.TypesInfo.Defs[id]; ok {
				return
			}

			pos := pkg.Fset.Position(id.Pos())
			key := fmt.Sprintf("%s:%s:%d", pkg.Types.Path(), result.Base(pos.Filename), pos.Line)
			note := expectNotes[key]

			switch {
			case result.IsNil[id.Name]:
				if note == nil || (note.Name != "isNil" && note.Name != "isZero") {
					t.Errorf("unexpected replacing nil (%s) in %v", id.Name, key)
				}
			case result.IsZero[id.Name]:
				if note == nil || note.Name != "isZero" {
					t.Errorf("unexpected replacing zero value (%s) in %v", id.Name, key)
				}
			}

			delete(expectNotes, key)
		})
	}

	sort.Strings(keys)
	for _, key := range keys {
		if note := expectNotes[key]; note != nil {
			t.Errorf("expected replacing did not occur: %v", note)
		}
	}
}
