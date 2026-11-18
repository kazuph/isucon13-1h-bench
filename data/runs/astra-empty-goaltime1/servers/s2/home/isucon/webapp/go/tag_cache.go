package main

import "sync/atomic"

// The tag catalog is immutable between initializations.
type tagCatalog struct{ body atomic.Pointer[[]byte] }

var activeTagCatalog atomic.Pointer[tagCatalog]

func init()            { clearTagCatalog() }
func clearTagCatalog() { activeTagCatalog.Store(&tagCatalog{}) }
