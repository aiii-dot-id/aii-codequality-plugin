package cq

import (
	"go/ast"
	"unsafe"
)

func sourceBytes(s string) []byte { return unsafe.Slice(unsafe.StringData(s), len(s)) }

func forget(f *ast.File) {
	if f == nil {
		return
	}
	for i := range f.Decls {
		f.Decls[i] = nil
	}
	for i := range f.Comments {
		f.Comments[i] = nil
	}
	f.Decls, f.Comments, f.Imports, f.Unresolved, f.Scope, f.Doc, f.Name = nil, nil, nil, nil, nil, nil, nil
}
