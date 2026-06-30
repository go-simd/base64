//go:build amd64

package base64

import (
	"bytes"
	stdb64 "encoding/base64"
	"math/rand"
	"testing"
)

// TestDecodeSIMDDispatch drives every branch of the amd64 decodeSIMD dispatcher:
// the AVX2 path (n>=44, hasAVX2Decode=true), the SSE path (n>=24, forced
// hasAVX2Decode=false), and the scalar-only return (small n). The AVX2 CI box has
// hasAVX2Decode always true, so the SSE/scalar branches are exercised by forcing
// the flag low. Every case must stay byte+error-identical to encoding/base64.
func TestDecodeSIMDDispatch(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	for _, ep := range encodings() {
		check := func(in string) {
			t.Helper()
			gotB, gotErr := ep.enc.DecodeString(in)
			wantB, wantErr := ep.ref.DecodeString(in)
			if !bytes.Equal(gotB, wantB) || !errEqual(gotErr, wantErr) {
				t.Fatalf("%s hasAVX2Decode=%v DecodeString(%q):\n got=%x err=%v\nwant=%x err=%v",
					ep.name, hasAVX2Decode, in, gotB, gotErr, wantB, wantErr)
			}
		}
		// A spread of valid encodings (multiple SIMD blocks + tail) plus an invalid
		// byte deep inside a block so a wrong bail offset would diverge.
		var cases []string
		for _, n := range []int{0, 1, 5, 12, 15, 16, 17, 18, 24, 32, 48, 60, 96, 200} {
			src := make([]byte, n)
			rng.Read(src)
			cases = append(cases, ep.ref.EncodeToString(src))
		}
		clean := ep.ref.EncodeToString(make([]byte, 120))
		for _, off := range []int{0, 15, 16, 31, 33, 50} {
			b := []byte(clean)
			b[off] = '!'
			cases = append(cases, string(b))
		}

		// Real CPU flag (AVX2 path on CI), then force the SSE + scalar branches.
		saved := hasAVX2Decode
		for _, in := range cases {
			check(in)
		}
		hasAVX2Decode = false
		for _, in := range cases {
			check(in)
		}
		hasAVX2Decode = saved
	}
}

func benchDecodeForce(b *testing.B, avx2 bool) {
	src := []byte(stdb64.StdEncoding.EncodeToString(benchData()))
	dst := make([]byte, DecodedLen(len(src)))
	saved := hasAVX2Decode
	hasAVX2Decode = avx2
	defer func() { hasAVX2Decode = saved }()
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Decode(dst, src)
	}
}

func BenchmarkDecodeForceSSE(b *testing.B)  { benchDecodeForce(b, false) }
func BenchmarkDecodeForceAVX2(b *testing.B) { benchDecodeForce(b, true) }
