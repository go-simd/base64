package base64

import (
	"encoding/base64"
	"math/rand"
	"testing"
)

func TestEncode(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{0, 1, 2, 3, 11, 12, 13, 15, 16, 17, 24, 47, 48, 49, 1000, 4096} {
		src := make([]byte, n)
		rng.Read(src)
		if got, want := EncodeToString(src), base64.StdEncoding.EncodeToString(src); got != want {
			t.Fatalf("n=%d:\n got=%q\nwant=%q", n, got, want)
		}
	}
}

func FuzzEncode(f *testing.F) {
	f.Add([]byte("hello world"))
	f.Fuzz(func(t *testing.T, src []byte) {
		if got, want := EncodeToString(src), base64.StdEncoding.EncodeToString(src); got != want {
			t.Fatalf("got=%q want=%q", got, want)
		}
	})
}

func benchData() []byte { b := make([]byte, 1<<20); rand.New(rand.NewSource(2)).Read(b); return b }

func BenchmarkEncode(b *testing.B) {
	src := benchData()
	dst := make([]byte, EncodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Encode(dst, src)
	}
}
func BenchmarkEncodeStdlib(b *testing.B) {
	src := benchData()
	dst := make([]byte, EncodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		base64.StdEncoding.Encode(dst, src)
	}
}
