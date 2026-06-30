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

// TestEncodeSIMDDispatch drives every branch of the amd64 encodeSIMD dispatcher:
// the AVX2 path (n>=32, hasAVX2=true), the SSE path (n>=16, hasAVX2=false), and
// the scalar-only return (n<16). hasAVX2 is forced low for the SSE/scalar cases
// because CI runs on a native AVX2 box where it is otherwise always true.
func TestEncodeSIMDDispatch(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, ep := range encodings() {
		check := func(n int) {
			src := make([]byte, n)
			rng.Read(src)
			if got, want := ep.enc.EncodeToString(src), ep.ref.EncodeToString(src); got != want {
				t.Fatalf("%s n=%d hasAVX2=%v:\n got=%q\nwant=%q", ep.name, n, hasAVX2, got, want)
			}
		}
		// AVX2 branch (and small sizes) with the real CPU flag, then force SSE+scalar
		// (hasAVX2 is always true on the AVX2 CI box).
		saved := hasAVX2
		for _, n := range []int{0, 8, 15, 16, 31, 32, 33, 64, 100} {
			check(n)
		}
		hasAVX2 = false
		for _, n := range []int{0, 8, 15, 16, 17, 31, 32, 33, 64, 100} {
			check(n)
		}
		hasAVX2 = saved
	}
}
