package findnil

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"io"
	"os"
	"sort"

	"github.com/gostaticanalysis/findnil/nilless"
	"golang.org/x/tools/go/ast/inspector"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/pointer"
	"golang.org/x/tools/go/ssa"
)

const (
	ExitSuccess = 0
	ExitError   = 1
)

func Main(args ...string) int {
	cmd := &Cmd{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	return cmd.Run(args...)
}

type Cmd struct {
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
}

func (cmd *Cmd) Run(args ...string) int {
	if err := cmd.run(args); err != nil {
		fmt.Fprintln(cmd.Stderr, "Error:", err)
		return ExitError
	}
	return ExitSuccess
}

func (cmd *Cmd) run(args []string) error {
	cfg := &packages.Config{
		Dir:  cmd.Dir,
		Fset: token.NewFileSet(),
		Mode: packages.NeedFiles | packages.NeedSyntax | packages.NeedTypesInfo |
			packages.NeedTypes | packages.NeedDeps | packages.NeedModule,
	}
	result, err := nilless.Load(cfg, args...)
	if err != nil {
		return err
	}

	prog, err := buildSSA(result)
	if err != nil {
		return err
	}

	if err := cmd.analyze(prog); err != nil {
		return err
	}

	return nil
}

func (cmd *Cmd) analyze(prog *Program) error {

	config := &pointer.Config{
		Mains: prog.Mains,
	}

	var nodes []ast.Node
	node2pkg := make(map[ast.Node]*types.Package)
	node2value := make(map[ast.Node]ssa.Value)

	for _, pkg := range prog.Packages {
		inspect := inspector.New(prog.Files[pkg])
		filter := []ast.Node{(*ast.SelectorExpr)(nil)}
		inspect.WithStack(filter, func(n ast.Node, push bool, stack []ast.Node) (proceed bool) {
			if !push {
				return false
			}

			sel, _ := n.(*ast.SelectorExpr)
			if sel == nil {
				return true
			}

			typ := prog.TypesInfo[pkg].TypeOf(sel.X)
			if !pointer.CanPoint(typ) {
				return false
			}

			f := ssa.EnclosingFunction(pkg, stackToPath(stack))
			if f == nil {
				return false
			}

			v, _ := f.ValueForExpr(sel.X)
			if v == nil {
				return false
			}

			nodes = append(nodes, sel)
			node2pkg[sel] = f.Package().Pkg
			node2value[sel] = v
			config.AddQuery(v)

			return true
		})
	}

	result, err := pointer.Analyze(config)
	if err != nil {
		return err
	}

	nils := make(map[ssa.Value]bool)
	done := make(map[ssa.Value]bool)
	for v, p := range result.Queries {
		if isNil(prog, done, v) {
			nils[v] = true
		}

		for _, l := range p.PointsTo().Labels() {
			lv := l.Value()
			if !(nils[v] && nils[lv]) || isNil(prog, done, lv) {
				nils[v] = true
				nils[lv] = true
			}
		}
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Pos() < nodes[i].Pos()
	})
	for _, n := range nodes {
		v := node2value[n]
		if !nils[v] {
			continue
		}

		pkg := node2pkg[n].Path()
		var buf bytes.Buffer
		format.Node(&buf, prog.Fset, n)
		fmt.Fprintf(cmd.Stdout, "%s %s may be nil\n", fileline(prog, pkg, n.Pos()), &buf)
	}

	return nil
}

func stackToPath(stack []ast.Node) []ast.Node {
	path := make([]ast.Node, len(stack))
	for i := range stack {
		path[len(path)-i-1] = stack[i]
	}
	return path
}

func refs(v ssa.Value) []ssa.Instruction {
	refsptr := v.Referrers()
	if refsptr == nil {
		return nil
	}
	return *refsptr
}

func isNil(prog *Program, done map[ssa.Value]bool, v ssa.Value) bool {
	if done[v] {
		return false
	}
	done[v] = true

	if isNilGlobal(prog, v) {
		return true
	}

	for _, ref := range refs(v) {
		switch ref := ref.(type) {
		case *ssa.DebugRef:
			id, _ := ref.Expr.(*ast.Ident)
			if id == nil {
				continue
			}

			if prog.Nilless.IsNil[id.Name] {
				return true
			}
		case *ssa.Store:
			return isNil(prog, done, ref.Val)
		}
	}

	switch v := v.(type) {
	case *ssa.UnOp:
		return isNil(prog, done, v.X)
	}

	return false
}

func isNilGlobal(prog *Program, v ssa.Value) bool {
	switch v := v.(type) {
	case *ssa.UnOp:
		return isNilGlobal(prog, v.X)
	case *ssa.Global:
		for _, init := range prog.TypesInfo[v.Pkg].InitOrder {
			if len(init.Lhs) != 1 || v.Object() != init.Lhs[0] {
				continue
			}

			id, _ := init.Rhs.(*ast.Ident)
			if id != nil && prog.Nilless.IsNil[id.Name] {
				return true
			}
		}
	}

	return false
}

func fileline(prog *Program, pkg string, p token.Pos) string {
	pos := prog.Fset.Position(p)
	fname := prog.Nilless.Base(pos.Filename)
	return fmt.Sprintf("%s:%d:%d", fname, pos.Line, pos.Column)
}
