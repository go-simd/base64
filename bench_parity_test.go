package base64

// Standardized performance-parity harness: go-simd/base64 (SIMD dispatch) vs
// encoding/base64 (stdlib, also the scalar tail) vs github.com/emmansun/base64
// (pure-Go SIMD reference) vs github.com/cristalhq/base64 (pure-Go scalar Turbo
// reference). Run:
//
//	GOWORK=off go test -run=^$ -bench='Parity' -benchmem .
//
// Every sub-benchmark sets b.SetBytes(len(src in encoding domain)) so `go test`
// reports MB/s directly. Sizes span small/medium/large buffers.

import (
	"encoding/base64"
	"math/rand"
	"testing"

	emmansun "github.com/emmansun/base64"
)

var paritySizes = []int{16, 64, 1024, 16384, 1 << 20}

func paritySrc(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(2)).Read(b)
	return b
}

func sizeLabel(n int) string {
	switch n {
	case 16:
		return "16B"
	case 64:
		return "64B"
	case 1024:
		return "1KiB"
	case 16384:
		return "16KiB"
	case 1 << 20:
		return "1MiB"
	}
	return "?"
}

func BenchmarkParityEncode(b *testing.B) {
	for _, n := range paritySizes {
		src := paritySrc(n)
		dst := make([]byte, EncodedLen(n))
		b.Run(sizeLabel(n)+"/gosimd", func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				Encode(dst, src)
			}
		})
		b.Run(sizeLabel(n)+"/stdlib", func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				base64.StdEncoding.Encode(dst, src)
			}
		})
		b.Run(sizeLabel(n)+"/emmansun", func(b *testing.B) {
			b.SetBytes(int64(n))
			for i := 0; i < b.N; i++ {
				emmansun.StdEncoding.Encode(dst, src)
			}
		})
		benchParityEncodeCristalhq(b, n, dst, src)
	}
}

func BenchmarkParityDecode(b *testing.B) {
	for _, n := range paritySizes {
		enc := []byte(base64.StdEncoding.EncodeToString(paritySrc(n)))
		dst := make([]byte, DecodedLen(len(enc)))
		b.Run(sizeLabel(n)+"/gosimd", func(b *testing.B) {
			b.SetBytes(int64(len(enc)))
			for i := 0; i < b.N; i++ {
				Decode(dst, enc)
			}
		})
		b.Run(sizeLabel(n)+"/stdlib", func(b *testing.B) {
			b.SetBytes(int64(len(enc)))
			for i := 0; i < b.N; i++ {
				base64.StdEncoding.Decode(dst, enc)
			}
		})
		b.Run(sizeLabel(n)+"/emmansun", func(b *testing.B) {
			b.SetBytes(int64(len(enc)))
			for i := 0; i < b.N; i++ {
				emmansun.StdEncoding.Decode(dst, enc)
			}
		})
	}
}
