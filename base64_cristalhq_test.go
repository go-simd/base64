//go:build !loong64

package base64

import (
	"encoding/base64"
	"testing"

	cristalhq "github.com/cristalhq/base64"
)

// checkCristalhq sanity-checks that the cristalhq benchmark competitor encodes
// byte-identically to the standard library, so the Performance table compares
// like for like. cristalhq v0.1.2 has no loong64 build (its per-arch ctou32/
// stou32 build-tag lists predate the loong64 port), so on loong64 this helper
// is replaced by the no-op in base64_cristalhq_loong64_test.go.
func checkCristalhq(t *testing.T, n int, src []byte) {
	t.Helper()
	if got, want := cristalhq.StdEncoding.EncodeToString(src), base64.StdEncoding.EncodeToString(src); got != want {
		t.Fatalf("cristalhq n=%d:\n got=%q\nwant=%q", n, got, want)
	}
}

// BenchmarkEncodeCristalhq benchmarks github.com/cristalhq/base64, a pure-Go
// scalar-optimised (Turbo-Base64, no SIMD assembly) drop-in.
func BenchmarkEncodeCristalhq(b *testing.B) {
	src := benchData()
	dst := make([]byte, cristalhq.StdEncoding.EncodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cristalhq.StdEncoding.Encode(dst, src)
	}
}
