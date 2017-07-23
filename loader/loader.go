package loader

import (
	"errors"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"log"
	"strings"

	"honnef.co/go/tools/ssa"

	"golang.org/x/tools/go/buildutil"
)

// FIXME(dh): when we reparse a package, new files get added to the
// FileSet. There is, however, no way of removing files from the
// FileSet, so it grows forever, leaking memory.

// FIXME(dh): go/ssa uses typeutil.Hasher, which grows monotonically –
// i.e. leaks memory over time.

type Package struct {
	*types.Package
	*types.Info

	Files map[*token.File]*ast.File
	SSA   *ssa.Package

	Dependencies        map[string]struct{}
	ReverseDependencies map[string]struct{}

	Explicit bool

	Program *Program

	dirty bool
}

func (a *Program) newPackage() *Package {
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
		Dependencies:        map[string]struct{}{},
		ReverseDependencies: map[string]struct{}{},
		Program:             a,
	}
}

type Program struct {
	Fset *token.FileSet
	// Packages maps import paths to type-checked packages.
	Packages     map[string]*Package
	TypePackages map[*types.Package]*Package
	SSA          *ssa.Program
	Build        build.Context

	checker *types.Config
	Errors  TypeErrors

	logDepth int
}

type TypeErrors []types.Error

func (TypeErrors) Error() string {
	return "type errors"
}

func NewProgram() *Program {
	fset := token.NewFileSet()
	a := &Program{
		Fset:         fset,
		Packages:     map[string]*Package{},
		TypePackages: map[*types.Package]*Package{},
		SSA:          ssa.NewProgram(fset, ssa.GlobalDebug),
		checker:      &types.Config{},
		Build:        build.Default,
	}
	a.checker.Importer = a
	a.checker.Error = func(err error) {
		a.Errors = append(a.Errors, err.(types.Error))
	}
	return a
}

func (a *Program) InitialPackages() []*Package {
	// TODO(dh): rename to ExplicitPackages
	var pkgs []*Package
	for _, pkg := range a.Packages {
		if pkg.Explicit {
			pkgs = append(pkgs, pkg)
		}
	}
	return pkgs
}

func (a *Program) Import(path string) (*types.Package, error) {
	return nil, nil
}

func (a *Program) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	bpkg, err := a.Build.Import(path, srcDir, 0)
	if err != nil {
		return nil, err
	}

	if pkg, ok := a.Packages[bpkg.ImportPath]; ok && !pkg.dirty {
		return pkg.Package, nil
	}
	// FIXME(dh): don't recurse forever on circular dependencies
	pkg, err := a.compile(path, srcDir)
	if err != nil {
		return nil, err
	}
	a.Packages[bpkg.ImportPath] = pkg
	a.TypePackages[pkg.Package] = pkg
	return pkg.Package, nil
}

func (a *Program) Package(path string) *Package {
	return a.Packages[path]
}

func (a *Program) Compile(path string) (*Package, error) {
	// TODO(dh): support cgo preprocessing a la go/loader
	//
	// TODO(dh): support scoping packages to their build tags
	//
	// TODO(dh): build packages in parallel
	//
	// TODO(dh): don't recompile up to date packages
	//
	// TODO(dh): remove stale reverse dependencies

	a.Errors = nil
	pkg, err := a.compile(path, ".")
	if a.Errors != nil {
		return nil, a.Errors
	}
	if err != nil {
		return nil, err
	}
	pkg.Explicit = true
	a.Packages[path] = pkg
	a.TypePackages[pkg.Package] = pkg
	return pkg, nil
}

func (a *Program) markDirty(pkg *Package) {
	pkg.dirty = true
	if pkg.SSA != nil {
		a.SSA.RemovePackage(pkg.SSA)
	}
	for rdep := range pkg.ReverseDependencies {
		// the package might not be cached yet if we're currently
		// importing its dependencies
		if rpkg := a.Package(rdep); rpkg != nil {
			a.markDirty(rpkg)
		}
	}
}

func (a *Program) RecompileDirtyPackages() error {
	for path, pkg := range a.Packages {
		if !pkg.dirty {
			continue
		}
		_, err := a.compile(path, ".")
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *Program) compile(path string, srcdir string) (*Package, error) {
	a.logDepth++
	defer func() { a.logDepth-- }()
	pkg := a.newPackage()
	old, ok := a.Packages[path]
	if ok {
		pkg.ReverseDependencies = old.ReverseDependencies
		pkg.Explicit = old.Explicit
	}
	delete(a.TypePackages, pkg.Package)

	log.Printf("%scompiling %s", strings.Repeat("\t", a.logDepth), path)
	// OPT(dh): when compile gets called while rebuilding dirty
	// packages, it is unnecessary to call markDirty. in fact, this
	// causes exponential complexity.
	if path == "unsafe" {
		pkg.Package = types.Unsafe
		pkg.dirty = false
		return pkg, nil
	}

	a.markDirty(pkg)

	var err error
	build, err := a.Build.Import(path, srcdir, 0)
	if err != nil {
		return nil, err
	}
	if len(build.CgoFiles) != 0 {
		return nil, errors.New("cgo is not currently supported")
	}

	pkg.Files = map[*token.File]*ast.File{}
	var files []*ast.File
	for _, f := range build.GoFiles {
		// TODO(dh): cache parsed files and only reparse them if
		// necessary
		af, err := buildutil.ParseFile(a.Fset, &a.Build, nil, build.Dir, f, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		tf := a.Fset.File(af.Pos())
		pkg.Files[tf] = af
		files = append(files, af)
	}

	pkg.Package, err = a.checker.Check(path, a.Fset, files, pkg.Info)
	if err != nil {
		return nil, err
	}
	pkg.SSA = a.SSA.CreatePackage(pkg.Package, files, pkg.Info, true)
	pkg.SSA.Build()

	for _, imp := range build.Imports {
		// OPT(dh): we're duplicating a lot of go/build lookups
		// between here and ImportFrom. Maybe we can cache them.
		bdep, err := a.Build.Import(imp, build.Dir, 0)
		if err != nil {
			// shouldn't happen
			return nil, err
		}
		dep := a.Package(bdep.ImportPath)
		pkg.Dependencies[bdep.ImportPath] = struct{}{}
		dep.ReverseDependencies[build.ImportPath] = struct{}{}
	}

	pkg.dirty = false
	return pkg, nil
}
