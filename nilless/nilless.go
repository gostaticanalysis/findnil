package nilless

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.uber.org/multierr"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/pointer"
	"golang.org/x/tools/go/types/typeutil"
	"golang.org/x/tools/imports"
)

type Result struct {
	tmpdir string
	Pkgs   []*packages.Package
	Fset   *token.FileSet
	IsNil  map[string]bool
	IsZero map[string]bool
}

func (r *Result) Base(path string) string {
	return strings.TrimPrefix(path, r.tmpdir)
}

func Load(cfg *packages.Config, patterns ...string) (_ *Result, rerr error) {
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "nilless-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		rerr = multierr.Append(rerr, os.RemoveAll(dir))
		if rerr != nil {
			rerr = fmt.Errorf("nilless.Load: %w", rerr)
		}
	}()

	r := &replacer{
		cfg:    cfg,
		pkgs:   pkgs,
		hasher: typeutil.MakeHasher(),
		dir:    dir,
		result: &Result{
			tmpdir: dir,
			IsNil:  make(map[string]bool),
			IsZero: make(map[string]bool),
		},
	}

	if len(r.pkgs) == 0 {
		r.result.Pkgs = r.pkgs
		return r.result, nil
	}

	var pkgerr error
	packages.Visit(r.pkgs, nil, func(pkg *packages.Package) {
		for _, err := range pkg.Errors {
			pkgerr = multierr.Append(pkgerr, err)
		}
	})
	if pkgerr != nil {
		return nil, pkgerr
	}

	for i := range r.pkgs {
		r.idx = i
		if err := r.do(); err != nil {
			return nil, err
		}
	}

	newCfg := *(r.cfg)
	newCfg.Dir = dir
	newCfg.Fset = token.NewFileSet()
	newPkgs, err := packages.Load(&newCfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("packages.Load: %w", err)
	}
	r.result.Pkgs = newPkgs
	r.result.Fset = newCfg.Fset

	return r.result, nil
}

type nilDecl struct {
	gendecl *ast.GenDecl
	name    string
}

type zeroDecl struct {
	funcdecl *ast.FuncDecl
	name     string
}

type replacer struct {
	cfg       *packages.Config
	idx       int
	pkgs      []*packages.Package
	hasher    typeutil.Hasher
	nilDecls  typeutil.Map // value is *nilDecl
	zeroDecls typeutil.Map // value is *zeroDecl
	dir       string
	result    *Result
}

func (r *replacer) do() error {

	newFiles := make([]*ast.File, len(r.pkgs[r.idx].Syntax))

	var err error
	for i, file := range r.pkgs[r.idx].Syntax {
		// TODO(tenntenn): more replacing
		// - composite literals
		// - naked returns
		n := astutil.Apply(file, func(c *astutil.Cursor) bool {
			switch n := c.Node().(type) {
			case *ast.ValueSpec:
				switch {
				// assignment
				case len(n.Names) == len(n.Values):
					err = multierr.Append(err, r.declAndAssign(c, n))
				// zero value
				case len(n.Values) == 0:
					err = multierr.Append(err, r.decl(c, n))
				}
			case *ast.ReturnStmt:
				err = multierr.Append(err, r.returnStmt(c, n))
			}
			return true

		}, nil)
		f, _ := n.(*ast.File)
		if f == nil {
			return fmt.Errorf("unexpected node type: %v", n)
		}
		newFiles[i] = f
	}
	if err != nil {
		return fmt.Errorf("nilless: %w", err)
	}

	if err := r.output(newFiles); err != nil {
		return fmt.Errorf("nilless: %w", err)
	}

	return nil
}

func (r *replacer) returnStmt(c *astutil.Cursor, ret *ast.ReturnStmt) error {
	newRet := &ast.ReturnStmt{
		Return:  ret.Return,
		Results: make([]ast.Expr, len(ret.Results)),
	}

	rets := r.funcByPos(ret.Pos()).Results()
	for i, val := range ret.Results {
		if !r.isNil(val) {
			newRet.Results[i] = val
			continue
		}
		typ := rets.At(i).Type()
		newVal, err := r.nilValue(typ)
		if err != nil {
			return err
		}
		newRet.Results[i] = newVal
	}

	c.Replace(newRet)

	return nil
}

func (r *replacer) funcByPos(pos token.Pos) (sig *types.Signature) {
	file := r.fileByPos(pos)
	if file == nil {
		return nil
	}
	path, _ := astutil.PathEnclosingInterval(file, pos, pos)
	for _, n := range path {
		switch n := n.(type) {
		case *ast.FuncDecl:
			sig, _ := r.pkgs[r.idx].TypesInfo.TypeOf(n.Name).(*types.Signature)
			return sig
		case *ast.FuncLit:
			sig, _ := r.pkgs[r.idx].TypesInfo.TypeOf(n).(*types.Signature)
			return sig
		}
	}
	return nil
}

func (r *replacer) fileByPos(pos token.Pos) *ast.File {
	for _, f := range r.pkgs[r.idx].Syntax {
		if f.Pos() <= pos && pos <= f.End() {
			return f
		}
	}
	return nil
}

func (r *replacer) declAndAssign(c *astutil.Cursor, spec *ast.ValueSpec) error {
	newSpec := &ast.ValueSpec{
		Doc:     spec.Doc,
		Names:   make([]*ast.Ident, len(spec.Names)),
		Values:  make([]ast.Expr, len(spec.Values)),
		Comment: spec.Comment,
	}
	copy(newSpec.Names, spec.Names)

	for i, val := range spec.Values {
		if !r.isNil(val) {
			newSpec.Values[i] = val
			continue
		}
		typ := r.pkgs[r.idx].TypesInfo.TypeOf(newSpec.Names[i])
		newVal, err := r.nilValue(typ)
		if err != nil {
			return err
		}
		newSpec.Values[i] = newVal
	}

	c.Replace(newSpec)

	return nil
}

func (r *replacer) isNil(expr ast.Expr) bool {
	id, _ := expr.(*ast.Ident)
	_, isNil := r.pkgs[r.idx].TypesInfo.ObjectOf(id).(*types.Nil)
	return isNil
}

func (r *replacer) nilValue(typ types.Type) (ast.Expr, error) {

	decl, _ := r.nilDecls.At(typ).(*nilDecl)
	if decl != nil {
		return ast.NewIdent(decl.name), nil
	}

	typExpr, err := parser.ParseExpr(r.typeString(typ))
	if err != nil {
		return nil, fmt.Errorf("parse type string(%s): %w", typ.String(), err)
	}

	name := uniqName(fmt.Sprintf("__nil_%p_%d_*", r.pkgs[r.idx], r.hasher.Hash(typ)), func(name string) bool {
		return r.pkgs[r.idx].Types.Scope().Lookup(name) == nil
	})

	decl = &nilDecl{
		gendecl: &ast.GenDecl{
			Tok: token.VAR,
			Specs: []ast.Spec{&ast.ValueSpec{
				Names: []*ast.Ident{ast.NewIdent(name)},
				Values: []ast.Expr{&ast.UnaryExpr{
					Op: token.MUL,
					X: &ast.CallExpr{
						Fun:  ast.NewIdent("new"),
						Args: []ast.Expr{typExpr},
					}}},
			}},
		},
		name: name,
	}

	r.nilDecls.Set(typ, decl)
	r.result.IsNil[name] = true

	return ast.NewIdent(decl.name), nil
}

func (r *replacer) declsFile() *ast.File {
	decls := make([]ast.Decl, 0, r.nilDecls.Len()+r.zeroDecls.Len())

	r.nilDecls.Iterate(func(_ types.Type, val interface{}) {
		if decl, _ := val.(*nilDecl); decl != nil {
			decls = append(decls, decl.gendecl)
		}
	})

	r.zeroDecls.Iterate(func(_ types.Type, val interface{}) {
		if decl, _ := val.(*zeroDecl); decl != nil {
			decls = append(decls, decl.funcdecl)
		}
	})

	file := &ast.File{
		Name:  ast.NewIdent(r.pkgs[r.idx].Name),
		Decls: decls,
	}

	for path := range r.pkgs[r.idx].Imports {
		astutil.AddImport(nil, file, path)
	}

	return file
}

func (r *replacer) output(files []*ast.File) error {

	if len(files) == 0 {
		return nil
	}

	// copy go.mod
	if err := r.copyGoMod(r.dir); err != nil {
		return err
	}

	if err := r.outputDecls(r.dir); err != nil {
		return err
	}

	for _, file := range files {
		if err := r.outputFile(r.dir, file); err != nil {
			return err
		}
	}

	return nil
}

func (r *replacer) copyGoMod(dir string) error {
	// without Go Modules
	if r.pkgs[r.idx].Module == nil {
		return nil
	}

	gomod, err := os.ReadFile(r.pkgs[r.idx].Module.GoMod)
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), gomod, 0o666); err != nil {
		return err
	}

	return nil
}

func (r *replacer) outputDecls(dir string) error {

	var buf bytes.Buffer

	fmt.Fprintln(&buf, "package", r.pkgs[r.idx].Syntax[0].Name.Name)

	for _, file := range r.pkgs[r.idx].Syntax {
		for _, impt := range file.Imports {
			fmt.Fprint(&buf, "import ")
			if impt.Name != nil {
				fmt.Fprint(&buf, impt.Name.Name+" ")
			}
			fmt.Fprintln(&buf, impt.Path.Value)
		}
	}

	var err error
	r.nilDecls.Iterate(func(_ types.Type, val interface{}) {
		if decl, _ := val.(*nilDecl); decl != nil {
			err = multierr.Append(err, format.Node(&buf, token.NewFileSet(), decl.gendecl))
			fmt.Fprintln(&buf)
		}
	})
	if err != nil {
		return err
	}

	r.zeroDecls.Iterate(func(_ types.Type, val interface{}) {
		if decl, _ := val.(*zeroDecl); decl != nil {
			err = multierr.Append(err, format.Node(&buf, token.NewFileSet(), decl.funcdecl))
			fmt.Fprintln(&buf)
		}
	})

	path := filepath.Join(dir, uniqName("nilless_decls_*.go", func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return os.IsNotExist(err)
	}))

	src, err := imports.Process("", buf.Bytes(), nil)
	if err != nil {
		fmt.Println(&buf)
		return fmt.Errorf("goimports %s: %w", path, err)
	}

	// for debug
	// fmt.Println(string(src))

	if err := os.WriteFile(path, src, 0o666); err != nil {
		return err
	}

	return nil
}

func (r *replacer) outputFile(dir string, file *ast.File) (rerr error) {
	name := filepath.Join(dir, filepath.Base(r.pkgs[r.idx].Fset.File(file.Pos()).Name()))
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	defer func() {
		rerr = multierr.Append(rerr, f.Close())
	}()

	var buf bytes.Buffer
	if err := format.Node(&buf, r.pkgs[r.idx].Fset, file); err != nil {
		return err
	}

	var w io.Writer = f
	// for debug
	// w = io.MultiWriter(f, os.Stdout)
	if _, err := io.Copy(w, &buf); err != nil {
		return err
	}

	return nil
}

func (r *replacer) decl(c *astutil.Cursor, spec *ast.ValueSpec) error {
	newSpec := &ast.ValueSpec{
		Doc:     spec.Doc,
		Names:   make([]*ast.Ident, len(spec.Names)),
		Values:  make([]ast.Expr, len(spec.Names)),
		Comment: spec.Comment,
	}
	copy(newSpec.Names, spec.Names)

	for i, name := range spec.Names {
		typ := r.pkgs[r.idx].TypesInfo.TypeOf(name)

		switch {
		case pointer.CanPoint(typ):
			val, err := r.nilValue(typ)
			if err != nil {
				return err
			}
			newSpec.Values[i] = val
		default:
			val, err := r.zeroValue(typ)
			if err != nil {
				return err
			}
			newSpec.Values[i] = val
		}
	}

	c.Replace(newSpec)

	return nil
}

func (r *replacer) zeroValue(typ types.Type) (ast.Expr, error) {
	decl, _ := r.zeroDecls.At(typ).(*zeroDecl)
	if decl != nil {
		return &ast.CallExpr{
			Fun: ast.NewIdent(decl.name),
		}, nil
	}

	typExpr, err := parser.ParseExpr(r.typeString(typ))
	if err != nil {
		return nil, fmt.Errorf("parse type string(%s): %w", typ.String(), err)
	}

	name := uniqName(fmt.Sprintf("__zero_%p_%d_*", r.pkgs[r.idx], r.hasher.Hash(typ)), func(name string) bool {
		return r.pkgs[r.idx].Types.Scope().Lookup(name) == nil
	})

	decl = &zeroDecl{
		funcdecl: &ast.FuncDecl{
			Name: ast.NewIdent(name),
			Type: &ast.FuncType{
				Params: &ast.FieldList{
					List: []*ast.Field{},
				},
				Results: &ast.FieldList{
					List: []*ast.Field{&ast.Field{
						Names: []*ast.Ident{ast.NewIdent("_")},
						Type:  typExpr,
					}},
				},
			},
			Body: &ast.BlockStmt{List: []ast.Stmt{new(ast.ReturnStmt)}},
		},
		name: name,
	}

	r.zeroDecls.Set(typ, decl)
	r.result.IsZero[name] = true

	return &ast.CallExpr{
		Fun: ast.NewIdent(decl.name),
	}, nil
}

func (r *replacer) typeString(typ types.Type) string {
	switch typ := typ.(type) {
	case *types.Named:
		return typ.Obj().Name()
	case *types.Pointer:
		return "*" + r.typeString(typ.Elem())
	case *types.Slice:
		return "[]" + r.typeString(typ.Elem())
	case *types.Array:
		return fmt.Sprintf("[%d]%s", typ.Len(), r.typeString(typ.Elem()))
	case *types.Map:
		return fmt.Sprintf("map[%s]%s", r.typeString(typ.Key()), r.typeString(typ.Elem()))
	case *types.Chan:
		switch typ.Dir() {
		case types.SendRecv:
			return fmt.Sprintf("chan %s", r.typeString(typ.Elem()))
		case types.SendOnly:
			return fmt.Sprintf("chan<- %s", r.typeString(typ.Elem()))
		case types.RecvOnly:
			return fmt.Sprintf("<-chan %s", r.typeString(typ.Elem()))
		}
	case *types.Signature:
		return "func" + r.signatureString(typ)
	case *types.Interface:
		methods := make([]string, 0, typ.NumMethods())
		for i := 0; i < typ.NumMethods(); i++ {
			m := typ.Method(i)
			sig, _ := m.Type().(*types.Signature)
			if sig == nil {
				continue
			}
			methods = append(methods, m.Name()+r.signatureString(sig))
		}
		return fmt.Sprintf("interface{ %s }", strings.Join(methods, ";"))
	case *types.Struct:
		fields := make([]string, typ.NumFields())
		for i := range fields {
			f := typ.Field(i)
			fields[i] = fmt.Sprintf("%s %s", f.Name(), r.typeString(f.Type()))
			if tag := typ.Tag(i); tag != "" {
				fields[i] += " " + strconv.Quote(tag)
			}
		}
		return fmt.Sprintf("struct{ %s }", strings.Join(fields, ";"))
	case *types.Basic:
		if typ.Info()&types.IsUntyped != 0 {
			return r.typeString(types.Default(typ))
		}
	}

	return typ.String()
}

func (r *replacer) signatureString(sig *types.Signature) string {
	args := make([]string, sig.Params().Len())
	results := make([]string, sig.Results().Len())

	for i := range args {
		args[i] = r.typeString(sig.Params().At(i).Type())
	}

	if sig.Variadic() {
		args[len(args)-1] = "..." + args[len(args)-1]
	}

	for i := range results {
		results[i] = r.typeString(sig.Results().At(i).Type())
	}

	return fmt.Sprintf("(%s) (%s)", strings.Join(args, ","), strings.Join(results, ","))
}
