package findnil

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/gostaticanalysis/findnil/nilless"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
)

type Program struct {
	Nilless   *nilless.Result
	SSA       *ssa.Program
	Packages  []*ssa.Package
	Mains     []*ssa.Package
	SrcFuncs  map[*ssa.Package][]*ssa.Function
	Fset      *token.FileSet
	TypesInfo map[*ssa.Package]*types.Info
	Files     map[*ssa.Package][]*ast.File
}

func buildSSA(result *nilless.Result) (*Program, error) {

	mode := ssa.GlobalDebug | ssa.NaiveForm | ssa.BareInits
	prog := &Program{
		Nilless:   result,
		SSA:       ssa.NewProgram(result.Fset, mode),
		SrcFuncs:  make(map[*ssa.Package][]*ssa.Function),
		Fset:      result.Fset,
		TypesInfo: make(map[*ssa.Package]*types.Info),
		Files:     make(map[*ssa.Package][]*ast.File),
	}

	// Create SSA packages for all imports.
	// Order is not significant.
	created := make(map[*packages.Package]bool)
	var createAll func(pkgs map[string]*packages.Package)
	createAll = func(pkgs map[string]*packages.Package) {
		for _, p := range pkgs {
			if !created[p] {
				created[p] = true
				ssapkg := prog.SSA.CreatePackage(p.Types, p.Syntax, p.TypesInfo, true)
				if p.Types.Name() == "main" {
					prog.Mains = append(prog.Mains, ssapkg)
				}
				prog.Files[ssapkg] = p.Syntax
				prog.TypesInfo[ssapkg] = p.TypesInfo
				prog.Packages = append(prog.Packages, ssapkg)
				createAll(p.Imports)
			}
		}
	}

	for _, pkg := range result.Pkgs {
		created[pkg] = true
		ssapkg := prog.SSA.CreatePackage(pkg.Types, pkg.Syntax, pkg.TypesInfo, true)
		if pkg.Types.Name() == "main" {
			prog.Mains = append(prog.Mains, ssapkg)
		}
		prog.Files[ssapkg] = pkg.Syntax
		prog.TypesInfo[ssapkg] = pkg.TypesInfo
		prog.Packages = append(prog.Packages, ssapkg)
		createAll(pkg.Imports)
	}

	prog.SSA.Build()

	for _, pkg := range prog.Packages {
		for _, f := range prog.Files[pkg] {
			for _, decl := range f.Decls {
				if fdecl, ok := decl.(*ast.FuncDecl); ok {

					if fdecl.Name.Name == "_" {
						continue
					}

					fn := prog.TypesInfo[pkg].Defs[fdecl.Name].(*types.Func)
					if fn == nil {
						return nil, fmt.Errorf("cannot get an object: %s", fdecl.Name.Name)
					}

					f := prog.SSA.FuncValue(fn)
					if f == nil {
						return nil, fmt.Errorf("cannot get a ssa function: %s", fdecl.Name.Name)
					}

					var addAnons func(f *ssa.Function)
					addAnons = func(f *ssa.Function) {
						prog.SrcFuncs[pkg] = append(prog.SrcFuncs[pkg], f)
						for _, anon := range f.AnonFuncs {
							addAnons(anon)
						}
					}
					addAnons(f)
				}
			}
		}
	}

	return prog, nil
}
