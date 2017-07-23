package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/scanner"
	"go/token"
	"go/types"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"unicode"

	"honnef.co/go/spew"
	"honnef.co/go/tools/loader"
	"honnef.co/go/tools/lsp"
	"honnef.co/go/tools/ssa"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/buildutil"
)

// TODO(dh): support non-ascii

var debug, _ = os.OpenFile("/tmp/out", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)

type Server struct {
	lprog   *loader.Program
	w       io.Writer
	overlay map[string][]byte
}

func (srv *Server) Notify(method string, v interface{}) error {
	msg := lsp.NotificationMessage{
		Message: lsp.Message{
			JSONRPC: "2.0",
		},
		Method: method,
		Params: v,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(srv.w, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err = srv.w.Write(payload)
	return err
}

func (srv *Server) Respond(req *lsp.RequestMessage, resp interface{}) error {
	msg := lsp.ResponseMessage{
		Message: lsp.Message{
			JSONRPC: "2.0",
		},
		ID:     req.ID,
		Result: resp,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(srv.w, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err = srv.w.Write(payload)
	return err
}

func (srv *Server) Error(req *lsp.RequestMessage, err error, code int) error {
	msg := lsp.ResponseMessage{
		Message: lsp.Message{
			JSONRPC: "2.0",
		},
		ID: req.ID,
		Error: &lsp.ResponseError{
			Code:    code,
			Message: err.Error(),
		},
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(srv.w, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err = srv.w.Write(payload)
	return err
}

type Position struct {
	Pkg  *loader.Package
	File *ast.File
	Pos  token.Pos
}

func (srv *Server) position(params *lsp.TextDocumentPositionParams) (Position, error) {
	f, err := buildutil.OpenFile(&srv.lprog.Build, params.TextDocument.URI.Path)
	if err != nil {
		return Position{}, err
	}
	data, err := ioutil.ReadAll(f)
	f.Close()
	if err != nil {
		return Position{}, err
	}
	n := 0
	for i := 0; i < params.Position.Line; i++ {
		// XXX handle no more lines
		n += bytes.IndexByte(data[n:], '\n') + 1
	}
	// XXX character is utf-16 offset, not byte offset
	n += params.Position.Character

	bpkg, err := buildutil.ContainingPackage(&srv.lprog.Build, ".", params.TextDocument.URI.Path)
	if err != nil {
		return Position{}, err
	}
	pkg := srv.lprog.Package(bpkg.ImportPath)
	var tf *token.File
	var af *ast.File
	for _, af = range pkg.Files {
		tf = srv.lprog.Fset.File(af.Pos())
		if tf.Name() == params.TextDocument.URI.Path {
			break
		}
	}
	if tf == nil {
		return Position{}, errors.New("file not found")
	}

	return Position{pkg, af, tf.Pos(n)}, nil
}

func (srv *Server) TextDocumentDefinition(params *lsp.TextDocumentPositionParams) ([]lsp.Location, error) {
	pos, err := srv.position(params)
	if err != nil {
		return nil, err
	}

	var node ast.Node
	path, _ := astutil.PathEnclosingInterval(pos.File, pos.Pos, pos.Pos)
	ident, ok := srv.identAtPosition(params)
	if ok {
		node = ident
	} else {
		node = path[0]
	}

	switch elem := node.(type) {
	case *ast.BasicLit:
		if len(path) < 2 {
			break
		}
		spec, ok := path[1].(*ast.ImportSpec)
		if !ok {
			break
		}
		path := spec.Path.Value[1 : len(spec.Path.Value)-1]
		dir := filepath.Dir(srv.lprog.Fset.File(pos.File.Pos()).Name())
		bpkg, err := srv.lprog.Build.Import(path, dir, build.FindOnly)
		if err != nil {
			break
		}
		// TODO(dh): go through the VFS
		names, err := filepath.Glob(filepath.Join(bpkg.Dir, "*.go"))
		if err != nil {
			log.Fatal(err)
		}
		var out []lsp.Location
		for _, name := range names {
			uri := &lsp.URI{
				Scheme: "file",
				Path:   name,
			}
			resp := lsp.Location{
				URI: uri,
			}
			out = append(out, resp)
		}
		spew.Fdump(os.Stderr, out)
		return out, nil
	case *ast.Ident:
		obj := pos.Pkg.ObjectOf(elem)
		if obj == nil {
			return nil, nil
		}
		target := srv.lprog.Fset.Position(obj.Pos())
		uri := &lsp.URI{
			Scheme: "file",
			Path:   target.Filename,
		}
		resp := lsp.Location{
			URI: uri,
			Range: lsp.Range{
				Start: lsp.Position{
					Line:      target.Line - 1,
					Character: target.Column - 1,
				},
				End: lsp.Position{
					Line:      target.Line - 1,
					Character: target.Column - 1,
				},
			},
		}
		return []lsp.Location{resp}, nil
	}
	return nil, nil
}

func writeTuple(tup *types.Tuple, variadic bool, qf types.Qualifier) []string {
	if tup == nil {
		return nil
	}
	var out []string
	for i := 0; i < tup.Len(); i++ {
		var str string
		v := tup.At(i)
		if v.Name() != "" {
			str += v.Name() + " "
		}
		typ := v.Type()
		if variadic && i == tup.Len()-1 {
			if s, ok := typ.(*types.Slice); ok {
				str += "..."
				typ = s.Elem()
			} else {
				// special case:
				// append(s, "foo"...) leads to signature func([]byte, string...)
				if t, ok := typ.Underlying().(*types.Basic); !ok || t.Kind() != types.String {
					panic("internal error: string type expected")
				}
				str += types.TypeString(typ, qf)
				str += "..."
				continue
			}
		}
		str += types.TypeString(typ, qf)
		out = append(out, str)
	}
	return out
}

func (srv *Server) TextDocumentSignatureHelp(params *lsp.TextDocumentPositionParams) (*lsp.SignatureHelp, error) {
	pos, err := srv.position(params)
	if err != nil {
		return nil, nil
	}
	var call *ast.CallExpr
	path, _ := astutil.PathEnclosingInterval(pos.File, pos.Pos, pos.Pos)
	for _, node := range path {
		var ok bool
		call, ok = node.(*ast.CallExpr)
		if ok {
			break
		}
	}
	if call == nil {
		return nil, nil
	}

	var ident *ast.Ident
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		ident = fn
	case *ast.SelectorExpr:
		ident = fn.Sel
	}
	var doc string
	if ident != nil {
		obj := pos.Pkg.ObjectOf(ident)
		af := srv.lprog.TypePackages[obj.Pkg()].Files[srv.lprog.Fset.File(obj.Pos())]
		path, _ := astutil.PathEnclosingInterval(af, obj.Pos(), obj.Pos())
		for _, node := range path {
			if node, ok := node.(*ast.FuncDecl); ok {
				doc = node.Doc.Text()
				break
			}
		}
	}

	sig, ok := pos.Pkg.TypeOf(call.Fun).(*types.Signature)
	if !ok {
		return nil, nil
	}
	// FIXME(dh): keep \n\n intact
	doc = strings.Replace(doc, "\n", " ", -1)
	lspsig := lsp.SignatureInformation{
		Label:         sig.String(),
		Documentation: doc,
	}
	args := writeTuple(sig.Params(), sig.Variadic(), nil)
	for _, arg := range args {
		lspsig.Parameters = append(lspsig.Parameters, lsp.ParameterInformation{
			Label: arg,
		})
	}

	var activeParam int
	for i, arg := range call.Args {
		if arg.Pos() < pos.Pos {
			activeParam = i
		}
	}

	if activeParam >= sig.Params().Len() {
		activeParam = sig.Params().Len() - 1
	}

	resp := &lsp.SignatureHelp{
		Signatures:      []lsp.SignatureInformation{lspsig},
		ActiveParameter: activeParam,
	}
	return resp, nil
}

func (srv *Server) TextDocumentSymbol(params *lsp.DocumentSymbolParams) ([]lsp.SymbolInformation, error) {
	bpkg, err := buildutil.ContainingPackage(&srv.lprog.Build, ".", params.TextDocument.URI.Path)
	if err != nil {
		return nil, err
	}
	pkg := srv.lprog.Package(bpkg.ImportPath)

	var info []lsp.SymbolInformation
	type object interface {
		Name() string
		Pos() token.Pos
	}
	addInfo := func(obj object, kind int, container string) {
		position := srv.lprog.Fset.Position(obj.Pos())
		if position.Filename != params.TextDocument.URI.Path {
			return
		}
		info = append(info, lsp.SymbolInformation{
			Name:          obj.Name(),
			Kind:          kind,
			ContainerName: container,
			Location: lsp.Location{
				URI: params.TextDocument.URI,
				Range: lsp.Range{
					Start: lsp.Position{
						Line:      position.Line - 1,
						Character: position.Column - 1,
					},
					End: lsp.Position{
						Line:      position.Line - 1,
						Character: position.Column - 1 + len(obj.Name()),
					},
				},
			},
		})
	}
	for _, member := range pkg.SSA.Members {
		switch member := member.(type) {
		case *ssa.NamedConst:
			addInfo(member, lsp.SymbolConstant, "")
		case *ssa.Global:
			addInfo(member, lsp.SymbolVariable, "")
		case *ssa.Function:
			addInfo(member, lsp.SymbolFunction, "")
		case *ssa.Type:
			addInfo(member, lsp.SymbolClass, "")

			T := member.Type().(*types.Named)
			for i := 0; i < T.NumMethods(); i++ {
				addInfo(T.Method(i), lsp.SymbolMethod, member.Name())
			}
			switch T := T.Underlying().(type) {
			case *types.Interface:
				for i := 0; i < T.NumExplicitMethods(); i++ {
					addInfo(T.ExplicitMethod(i), lsp.SymbolFunction, member.Name())
				}
			case *types.Struct:
				for i := 0; i < T.NumFields(); i++ {
					addInfo(T.Field(i), lsp.SymbolField, member.Name())
				}
			}
		}
	}

	return info, nil
}

func (srv *Server) identAtPosition(params *lsp.TextDocumentPositionParams) (*ast.Ident, bool) {
	pos, err := srv.position(params)
	if err != nil {
		log.Fatal(err)
	}
	f, err := buildutil.OpenFile(&srv.lprog.Build, params.TextDocument.URI.Path)
	if err != nil {
		log.Fatal(err)
	}
	data, err := ioutil.ReadAll(f)
	f.Close()
	if err != nil {
		log.Fatal(err)
	}

	off := srv.lprog.Fset.File(pos.Pos).Offset(pos.Pos)
	// XXX support non-ascii
	if !unicode.IsLetter(rune(data[off])) && !unicode.IsDigit(rune(data[off])) {
		pos.Pos--
	}

	path, _ := astutil.PathEnclosingInterval(pos.File, pos.Pos, pos.Pos)
	ident, ok := path[0].(*ast.Ident)
	return ident, ok
}

func (srv *Server) TextDocumentHighlight(params *lsp.TextDocumentPositionParams) ([]lsp.DocumentHighlight, error) {
	pos, err := srv.position(params)
	if err != nil {
		log.Fatal(err)
	}
	ident, ok := srv.identAtPosition(params)
	if !ok {
		return nil, nil
	}
	obj := pos.Pkg.ObjectOf(ident)
	if obj == nil {
		return nil, nil
	}
	var hls []lsp.DocumentHighlight
	ast.Inspect(pos.File, func(node ast.Node) bool {
		// OPT(dh): we could optimize this by starting the walk at the
		// scope surrounding the identifier.
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		if obj == pos.Pkg.ObjectOf(ident) {
			pos := srv.lprog.Fset.Position(ident.Pos())
			// TODO(dh): LSP differentiates between textual, read and
			// write accesses to variables. right now we're reporting
			// them all as textual matches.
			hl := lsp.DocumentHighlight{
				Range: lsp.Range{
					Start: lsp.Position{
						Line:      pos.Line - 1,
						Character: pos.Column - 1,
					},
					End: lsp.Position{
						Line:      pos.Line - 1,
						Character: pos.Column + len(ident.Name) - 1,
					},
				},
			}
			hls = append(hls, hl)
		}
		return true
	})
	return hls, nil
}

func (srv *Server) compilePackage(filename string) {
	bpkg, err := buildutil.ContainingPackage(&srv.lprog.Build, ".", filename)
	if err != nil {
		log.Println(err)
		return
	}
	_, err = srv.lprog.Compile(bpkg.ImportPath)
	diags := []lsp.Diagnostic{}
	switch err := err.(type) {
	case loader.TypeErrors:
		for _, err := range err {
			pos := err.Fset.Position(err.Pos)
			lsppos := lsp.Position{
				Line:      pos.Line - 1,
				Character: pos.Column - 1,
			}
			diag := lsp.Diagnostic{
				Range: lsp.Range{
					Start: lsppos,
					End:   lsppos,
				},
				Severity: lsp.Error,
				Source:   "compile",
				Message:  err.Msg,
			}
			diags = append(diags, diag)
		}
	case scanner.ErrorList:
		for _, err := range err {
			lsppos := lsp.Position{
				Line:      err.Pos.Line - 1,
				Character: err.Pos.Column - 1,
			}
			diag := lsp.Diagnostic{
				Range: lsp.Range{
					Start: lsppos,
					End:   lsppos,
				},
				Severity: lsp.Error,
				Source:   "compile",
				Message:  err.Msg,
			}
			diags = append(diags, diag)
		}
	case nil:
	default:
		log.Println(err)
		return
	}
	// XXX handle assigning diags to files
	params := lsp.PublishDiagnosticsParams{
		URI: &lsp.URI{
			Scheme: "file",
			Path:   filename,
		},
		Diagnostics: diags,
	}
	srv.Notify("textDocument/publishDiagnostics", params)
}

func (srv *Server) Initialize(params *lsp.InitializeParams) (*lsp.InitializeResult, error) {
	return &lsp.InitializeResult{
		Capabilities: lsp.ServerCapabilities{
			TextDocumentSync:   lsp.SyncFull,
			DefinitionProvider: true,
			DocumentLinkProvider: lsp.DocumentLinkOptions{
				ResolveProvider: false,
			},
			SignatureHelpProvider: lsp.SignatureHelpOptions{
				TriggerCharacters: []string{"(", ","},
			},
			DocumentSymbolProvider:    true,
			DocumentHighlightProvider: true,
		}}, nil
}

func (srv *Server) TextDocumentDidOpen(params *lsp.DidOpenTextDocumentParams) {
	srv.overlay[params.TextDocument.URI.Path] = []byte(params.TextDocument.Text)
	srv.compilePackage(params.TextDocument.URI.Path)
}

func (srv *Server) TextDocumentDidChange(params *lsp.DidChangeTextDocumentParams) {
	srv.overlay[params.TextDocument.URI.Path] = []byte(params.ContentChanges[0].Text)
	srv.compilePackage(params.TextDocument.URI.Path)
}

func main() {
	if err := syscall.Dup2(int(debug.Fd()), 2); err != nil {
		log.Fatal("dup failed:", err)
	}

	r := io.TeeReader(os.Stdin, os.Stderr)
	rw := bufio.NewReader(r)

	srv := &Server{w: os.Stdout}
	srv.overlay = map[string][]byte{}
	srv.lprog = loader.NewProgram()
	srv.lprog.Build = *buildutil.OverlayContext(&build.Default, srv.overlay)
	// l := lint.Linter{
	// 	Checker: staticcheck.NewChecker(),
	// }
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}
		if line != "\r\n" {
			continue
		}
		msg := &lsp.RequestMessage{}
		if err := json.NewDecoder(rw).Decode(&msg); err != nil {
			log.Fatal(err)
		}

		handlers := map[string]interface{}{
			"initialize":                     srv.Initialize,
			"textDocument/didOpen":           srv.TextDocumentDidOpen,
			"textDocument/didChange":         srv.TextDocumentDidChange,
			"textDocument/definition":        srv.TextDocumentDefinition,
			"textDocument/signatureHelp":     srv.TextDocumentSignatureHelp,
			"textDocument/documentSymbol":    srv.TextDocumentSymbol,
			"textDocument/documentHighlight": srv.TextDocumentHighlight,
		}
		fn := handlers[msg.Method]
		if fn == nil {
			srv.Error(msg, fmt.Errorf("method %s not found", msg.Method), lsp.MethodNotFound)
			continue
		}

		T := reflect.TypeOf(fn)
		v := reflect.ValueOf(fn)
		arg := reflect.New(T.In(0).Elem())
		if err := json.Unmarshal(msg.Params, arg.Interface()); err != nil {
			srv.Error(msg, err, lsp.ParseError)
			continue
		}
		ret := v.Call([]reflect.Value{arg})
		if len(ret) == 2 {
			if !ret[1].IsNil() {
				srv.Error(msg, ret[1].Interface().(error), lsp.InternalError)
				continue
			}
			srv.Respond(msg, ret[0].Interface())
		}
	}
}

func fileOffsetToPos(file *token.File, startOffset, endOffset int) (start, end token.Pos, err error) {
	// Range check [start..end], inclusive of both end-points.

	if 0 <= startOffset && startOffset <= file.Size() {
		start = file.Pos(startOffset)
	} else {
		err = fmt.Errorf("start position is beyond end of file")
		return
	}

	if 0 <= endOffset && endOffset <= file.Size() {
		end = file.Pos(endOffset)
	} else {
		err = fmt.Errorf("end position is beyond end of file")
		return
	}

	return
}
