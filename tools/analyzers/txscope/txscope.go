// Adapted from Porch tools/analyzers/txscope (Apache-2.0), itself adapted from HallPass.

// Package txscope reports blocking or external work inside store write transactions (ADR-001).
//
// A write transaction is the function literal passed to a method named Tx or TxNamed on a type declared in a package
// whose path ends in "/internal/store", and any function taking a *store.Tx. Inside it the analyzer forbids calls
// into network, NATS/Pulse, S3, outbound-HTTP, connector and process packages, time.Sleep, file fsyncs, channel
// operations and select statements.
package txscope

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer is the txscope analyzer.
var Analyzer = &analysis.Analyzer{
	Name:     "txscope",
	Doc:      "forbid network I/O, waits, fsyncs and external calls inside store write transactions",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

// bannedPkgs are import paths (exact or prefix followed by "/") that must not be called inside a transaction.
var bannedPkgs = []string{
	"net",
	"net/http",
	"os/exec",
	"github.com/nats-io/nats.go",
	"github.com/aurimasniekis/go-pulse",
	"github.com/minio/minio-go/v7",
	"github.com/coreos/go-oidc/v3",
}

// pureNetPkgs are net subpackages with no I/O (value types and parsing); they are allowed inside transactions.
var pureNetPkgs = map[string]bool{"net/netip": true, "net/url": true}

// bannedInternal are module-relative package suffixes that must not be called inside a transaction.
var bannedInternal = []string{
	"/internal/safehttp",
	"/internal/connectors",
	"/internal/agent",
	"/internal/events",
	"/pkg/deedclient",
}

func run(pass *analysis.Pass) (any, error) {
	ins := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	checked := map[*ast.BlockStmt]bool{}
	check := func(body *ast.BlockStmt) {
		if body != nil && !checked[body] {
			checked[body] = true
			checkBody(pass, body)
		}
	}
	ins.Preorder([]ast.Node{(*ast.CallExpr)(nil), (*ast.FuncLit)(nil), (*ast.FuncDecl)(nil)}, func(n ast.Node) {
		switch x := n.(type) {
		case *ast.CallExpr:
			// Literals passed directly to a store Tx method.
			if isStoreTx(pass, x) {
				for _, arg := range x.Args {
					if lit, ok := arg.(*ast.FuncLit); ok {
						check(lit.Body)
					}
				}
			}
		case *ast.FuncLit:
			// Any function taking the transaction handle runs inside a transaction: bodies routed through wrappers
			// (withPolicy), hooks and helpers.
			if isTxBody(pass, x.Type) {
				check(x.Body)
			}
		case *ast.FuncDecl:
			if isTxBody(pass, x.Type) {
				check(x.Body)
			}
		}
	})
	return nil, nil
}

// isTxBody reports whether a function has a parameter of type *Tx declared in a package whose path ends in
// "/internal/store".
func isTxBody(pass *analysis.Pass, ft *ast.FuncType) bool {
	if ft == nil || ft.Params == nil {
		return false
	}
	for _, f := range ft.Params.List {
		t := pass.TypesInfo.TypeOf(f.Type)
		p, ok := t.(*types.Pointer)
		if !ok {
			continue
		}
		named, ok := p.Elem().(*types.Named)
		if ok && named.Obj().Name() == "Tx" && named.Obj().Pkg() != nil && strings.HasSuffix(named.Obj().Pkg().Path(), "/internal/store") {
			return true
		}
	}
	return false
}

func isStoreTx(pass *analysis.Pass, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Tx" && !strings.HasPrefix(sel.Sel.Name, "Tx")) {
		return false
	}
	fn, ok := pass.TypesInfo.Uses[sel.Sel].(*types.Func)
	if !ok {
		return false
	}
	recv := fn.Signature().Recv()
	if recv == nil {
		return false
	}
	t := recv.Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return strings.HasSuffix(named.Obj().Pkg().Path(), "/internal/store")
}

func checkBody(pass *analysis.Pass, body *ast.BlockStmt) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectStmt:
			pass.Reportf(x.Pos(), "select statement inside store transaction: never wait while holding the writer")
		case *ast.UnaryExpr:
			if x.Op == token.ARROW {
				pass.Reportf(x.Pos(), "channel receive inside store transaction: never wait while holding the writer")
			}
		case *ast.SendStmt:
			pass.Reportf(x.Pos(), "channel send inside store transaction: publish events after commit")
		case *ast.CallExpr:
			// Callbacks registered with AfterCommit run after the transaction; they may publish and do I/O.
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "AfterCommit" {
				return false
			}
			checkCall(pass, x)
		}
		return true
	})
}

func checkCall(pass *analysis.Pass, call *ast.CallExpr) {
	var id *ast.Ident
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		id = f.Sel
	case *ast.Ident:
		id = f
	default:
		return
	}
	fn, ok := pass.TypesInfo.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return
	}
	path := fn.Pkg().Path()
	switch {
	case path == "time" && fn.Name() == "Sleep":
		pass.Reportf(call.Pos(), "time.Sleep inside store transaction")
		return
	case path == "os" && fn.Name() == "Sync":
		pass.Reportf(call.Pos(), "file fsync inside store transaction: fsync before or after the transaction")
		return
	}
	if pureNetPkgs[path] {
		return
	}
	for _, b := range bannedPkgs {
		if path == b || strings.HasPrefix(path, b+"/") {
			pass.Reportf(call.Pos(), "call to %s.%s inside store transaction: no network I/O while holding the writer", path, fn.Name())
			return
		}
	}
	if takesStoreTx(fn) {
		// Transaction-scoped helpers receive the transaction explicitly (for example an outbox enqueue): they are designed to run inside it and do only database work.
		return
	}
	if fn.Pkg() == pass.Pkg {
		// A package's own helpers: their bodies are checked when they take the transaction; banned-package rules
		// target calls from other packages (a same-package helper that does I/O without the transaction handle is a
		// documented blind spot).
		return
	}
	for _, b := range bannedInternal {
		if strings.HasSuffix(path, b) || strings.Contains(path, b+"/") {
			pass.Reportf(call.Pos(), "call to %s.%s inside store transaction: external side effects must run after commit", path, fn.Name())
			return
		}
	}
}

// takesStoreTx reports whether fn has a parameter that is a pointer to a type declared in an internal/store package.
func takesStoreTx(fn *types.Func) bool {
	params := fn.Signature().Params()
	for i := range params.Len() {
		p, ok := params.At(i).Type().(*types.Pointer)
		if !ok {
			continue
		}
		if named, ok := p.Elem().(*types.Named); ok && named.Obj().Pkg() != nil && strings.HasSuffix(named.Obj().Pkg().Path(), "/internal/store") {
			return true
		}
	}
	return false
}
