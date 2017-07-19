package augur

import (
	"errors"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"

	"honnef.co/go/tools/ssa"
)

type Package struct {
	*types.Package
	*types.Info

	SSA *ssa.Package

	Build *build.Package
}

func newPackage() *Package {
	return &Package{
		Info: &types.Info{
			Types:      map[ast.Expr]types.TypeAndValue{},
			Defs:       map[*ast.Ident]types.Object{},
			Uses:       map[*ast.Ident]types.Object{},
			Implicits:  map[ast.Node]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
			Scopes:     map[ast.Node]*types.Scope{},
			InitOrder:  []*types.Initializer{},
		},
	}
}

type Augur struct {
	Fset *token.FileSet
	// Packages maps import paths to type-checked packages.
	Packages map[string]*Package
	SSA      *ssa.Program

	checker *types.Config
	build   build.Context
}

func NewAugur() *Augur {
	fset := token.NewFileSet()
	a := &Augur{
		Fset:     fset,
		Packages: map[string]*Package{},
		SSA:      ssa.NewProgram(fset, ssa.GlobalDebug),
		checker:  &types.Config{},
		build:    build.Default,
	}
	a.checker.Importer = a
	return a
}

func (a *Augur) Import(path string) (*types.Package, error) {
	return nil, nil
}

func (a *Augur) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	// FIXME(dh): support vendoring
	pkg, ok := a.Packages[path]
	if ok {
		return pkg.Package, nil
	}
	// FIXME(dh): don't recurse forever on circular dependencies
	pkg, err := a.Compile(path)
	return pkg.Package, err
}

func (a *Augur) Compile(path string) (*Package, error) {
	// TODO(dh): support cgo preprocessing a la go/loader
	//
	// TODO(dh): support scoping packages to their build tags
	//
	// TODO(dh): rebuild reverse dependencies
	//
	// TODO(dh): build packages in parallel

	pkg := newPackage()
	err := a.compile(path, pkg)
	return pkg, err
}

func (a *Augur) compile(path string, pkg *Package) error {
	if path == "unsafe" {
		pkg.Package = types.Unsafe
		a.Packages[path] = pkg
		return nil
	}

	var err error
	pkg.Build, err = a.build.Import(path, ".", 0)
	if err != nil {
		return err
	}
	if len(pkg.Build.CgoFiles) != 0 {
		return errors.New("cgo is not currently supported")
	}

	var files []*ast.File
	for _, f := range pkg.Build.GoFiles {
		// TODO(dh): cache parsed files and only reparse them if
		// necessary
		af, err := parser.ParseFile(a.Fset, filepath.Join(pkg.Build.Dir, f), nil, parser.ParseComments)
		if err != nil {
			return err
		}
		files = append(files, af)
	}

	pkg.Package, err = a.checker.Check(path, a.Fset, files, pkg.Info)
	if err != nil {
		return err
	}
	a.Packages[path] = pkg
	pkg.SSA = a.SSA.CreatePackage(pkg.Package, files, pkg.Info, true)
	pkg.SSA.Build()

	return nil
}
