//go:build loong64

package base64

import "testing"

// benchParityEncodeCristalhq is a no-op on loong64: github.com/cristalhq/base64
// v0.1.2 has no loong64 build (its per-arch ctou32/stou32 build-tag lists predate
// the loong64 port), so it cannot be compiled into the test binary here. The
// cristalhq parity lane runs on every other arch (see
// bench_parity_cristalhq_test.go); loong64 still measures gosimd/stdlib/emmansun.
func benchParityEncodeCristalhq(b *testing.B, n int, dst, src []byte) {}
