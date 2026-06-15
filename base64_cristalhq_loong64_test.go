//go:build loong64

package base64

import "testing"

// checkCristalhq is a no-op on loong64: github.com/cristalhq/base64 v0.1.2 has
// no loong64 build (its per-arch ctou32/stou32 build-tag lists predate the
// loong64 port), so it cannot be compiled into the test binary here. The
// cristalhq differential cross-check and BenchmarkEncodeCristalhq run on every
// other arch (see base64_cristalhq_test.go); loong64 still exercises the full
// generic encode/decode path via the rest of the test suite.
func checkCristalhq(t *testing.T, n int, src []byte) {}
