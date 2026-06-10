//go:build amd64

package base64

import (
	stdb64 "encoding/base64"
	"math/rand"
	"testing"
)

func encodeForce(dst, src []byte, avx2 bool) {
	n := len(src)
	if avx2 && n >= 32 {
		g := (n-32)/24 + 1
		encodeBlocksAVX2(dst, src, g)
		stdb64.StdEncoding.Encode(dst[g*32:], src[g*24:])
		return
	}
	if n >= 16 {
		g := (n-16)/12 + 1
		encodeBlocksSSE(dst, src, g)
		stdb64.StdEncoding.Encode(dst[g*16:], src[g*12:])
		return
	}
	stdb64.StdEncoding.Encode(dst, src)
}

func benchForce(b *testing.B, avx2 bool) {
	src := make([]byte, 1<<20)
	rand.New(rand.NewSource(2)).Read(src)
	dst := make([]byte, EncodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encodeForce(dst, src, avx2)
	}
}

func BenchmarkEncodeForceSSE(b *testing.B)  { benchForce(b, false) }
func BenchmarkEncodeForceAVX2(b *testing.B) { benchForce(b, true) }
