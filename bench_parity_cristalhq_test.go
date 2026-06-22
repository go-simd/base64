//go:build !loong64

package base64

import (
	"testing"

	cristalhq "github.com/cristalhq/base64"
)

// benchParityEncodeCristalhq runs the cristalhq (pure-Go scalar "Turbo")
// competitor sub-benchmark. It lives in a !loong64 file because
// github.com/cristalhq/base64 v0.1.2 has no loong64 build (its per-arch
// ctou32/stou32 build-tag lists predate the loong64 port), so importing it would
// break the loong64 test binary; loong64 still runs every other parity lane via
// the no-op stub in bench_parity_cristalhq_loong64_test.go.
func benchParityEncodeCristalhq(b *testing.B, n int, dst, src []byte) {
	b.Run(sizeLabel(n)+"/cristalhq", func(b *testing.B) {
		b.SetBytes(int64(n))
		for i := 0; i < b.N; i++ {
			cristalhq.StdEncoding.Encode(dst, src)
		}
	})
}
