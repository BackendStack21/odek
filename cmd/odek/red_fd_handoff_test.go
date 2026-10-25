package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
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
	for _, f := range files {
		for _, v := range fdHandoffViolations(t, f, nil) {
			t.Error(v)
		}
	}
}

// The checker itself must flag a raw handoff, however the number reaches
// the env var, and accept a dup.
func TestRED_FDHandoffChecker_FlagsRawAndAcceptsDup(t *testing.T) {
	const src = `package main

func rawDirect(t *testing.T, r *os.File) { t.Setenv(keyFDEnvVar, fmt.Sprint(r.Fd())) }

func rawViaVariable(t *testing.T, r *os.File) {
	fd := int(r.Fd())
	t.Setenv(frameFDEnvVar, strconv.Itoa(fd))
}

func rawInClosure(t *testing.T, r *os.File) {
	f := func() string { return fmt.Sprint(r.Fd()) }
	os.Setenv(keyFDEnvVar, f())
}

func viaDupFD(t *testing.T, r *os.File) { t.Setenv(keyFDEnvVar, dupFD(t, r)) }

func viaSyscallDup(t *testing.T, r *os.File) {
	fd, _ := syscall.Dup(int(r.Fd()))
	t.Setenv(keyFDEnvVar, strconv.Itoa(fd))
}

func unrelated(r *os.File) uintptr { return r.Fd() }
`
	got := fdHandoffViolations(t, "fixture_test.go", src)
	want := []string{"rawDirect", "rawViaVariable", "rawInClosure"}
	if len(got) != len(want) {
		t.Fatalf("violations = %q, want one each for %v", got, want)
	}
	for i, name := range want {
		if !strings.Contains(got[i], " "+name+" ") {
			t.Errorf("violation %d = %q, want it to name %s", i, got[i], name)
		}
	}
}

// fdHandoffViolations parses one test file (or src when non-nil) and
// describes every .Fd() call outside syscall.Dup/dupFD in a function that
// sets the key or frame fd env var.
func fdHandoffViolations(t *testing.T, name string, src any) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name == "dupFD" || !setsFDEnvVar(fn.Body) {
			continue
		}
		for _, pos := range rawFdCalls(fn.Body) {
			out = append(out, fmt.Sprintf("%s: %s reads an owned descriptor with .Fd() outside syscall.Dup/dupFD while handing fds to the key/frame reader",
				fset.Position(pos), fn.Name.Name))
		}
	}
	return out
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
