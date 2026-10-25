package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// readKeyFromInheritedFD and readFrameNonceFromInheritedFD take ownership of
// the descriptor named in their env var and close it. A test that names a
// descriptor still owned by an *os.File gets it closed twice: the file's
// finalizer later closes whatever the number has been reused for — in a
// parallel test often the directory fd os.RemoveAll is walking, which then
// fails with "unlinkat: bad file descriptor". So in every test function that
// sets one of those env vars, each .Fd() call must sit inside syscall.Dup or
// dupFD: the reader is only ever handed a descriptor of its own, however the
// number reaches the env var.
func TestRED_FDHandoffTestsPassADuplicatedDescriptor(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "dupFD" || !setsFDEnvVar(fn.Body) {
				continue
			}
			for _, pos := range rawFdCalls(fn.Body) {
				t.Errorf("%s: %s reads an owned descriptor with .Fd() outside syscall.Dup/dupFD while handing fds to the key/frame reader",
					fset.Position(pos), fn.Name.Name)
			}
		}
	}
}

// setsFDEnvVar reports whether body calls t.Setenv(keyFDEnvVar|frameFDEnvVar, …).
func setsFDEnvVar(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Setenv" {
			if id, ok := call.Args[0].(*ast.Ident); ok && (id.Name == "keyFDEnvVar" || id.Name == "frameFDEnvVar") {
				found = true
			}
		}
		return !found
	})
	return found
}

// rawFdCalls returns the positions of .Fd() calls in body that are not
// nested inside a syscall.Dup(…) or dupFD(…) call.
func rawFdCalls(body *ast.BlockStmt) []token.Pos {
	var out []token.Pos
	var walk func(n ast.Node, dup bool)
	walk = func(n ast.Node, dup bool) {
		ast.Inspect(n, func(c ast.Node) bool {
			call, ok := c.(*ast.CallExpr)
			if !ok {
				return true
			}
			if isDupCall(call) && !dup {
				for _, a := range call.Args {
					walk(a, true)
				}
				return false
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Fd" && len(call.Args) == 0 && !dup {
				out = append(out, call.Pos())
			}
			return true
		})
	}
	walk(body, false)
	return out
}

func isDupCall(call *ast.CallExpr) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == "dupFD"
	case *ast.SelectorExpr:
		pkg, ok := fun.X.(*ast.Ident)
		return ok && pkg.Name == "syscall" && fun.Sel.Name == "Dup"
	}
	return false
}
