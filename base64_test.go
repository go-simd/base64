package base64

import (
	"bytes"
	"encoding/base64"
	"math/rand"
	"testing"

	emmansun "github.com/emmansun/base64"
)

func TestEncode(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{0, 1, 2, 3, 11, 12, 13, 15, 16, 17, 24, 47, 48, 49, 1000, 4096} {
		src := make([]byte, n)
		rng.Read(src)
		if got, want := EncodeToString(src), base64.StdEncoding.EncodeToString(src); got != want {
			t.Fatalf("n=%d:\n got=%q\nwant=%q", n, got, want)
		}
		// Sanity-check the benchmark competitors are byte-identical too, so the
		// Performance table compares like for like.
		if got, want := emmansun.StdEncoding.EncodeToString(src), base64.StdEncoding.EncodeToString(src); got != want {
			t.Fatalf("emmansun n=%d:\n got=%q\nwant=%q", n, got, want)
		}
		// cristalhq has no loong64 build (its arch tag lists predate loong64), so
		// this differential cross-check lives in a build-tagged helper that is a
		// no-op there. See base64_cristalhq_test.go.
		checkCristalhq(t, n, src)
	}
}

func TestDecode(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for _, n := range []int{0, 1, 2, 3, 11, 12, 13, 15, 16, 17, 24, 47, 48, 49, 1000, 4096} {
		src := make([]byte, n)
		rng.Read(src)
		enc := EncodeToString(src)

		// DecodedLen / DecodeString.
		if got := DecodedLen(len(enc)); got < n {
			t.Fatalf("n=%d: DecodedLen=%d < %d", n, got, n)
		}
		got, err := DecodeString(enc)
		if err != nil {
			t.Fatalf("n=%d: DecodeString: %v", n, err)
		}
		if string(got) != string(src) {
			t.Fatalf("n=%d: DecodeString round-trip mismatch", n)
		}

		// Decode into a caller-supplied buffer.
		dst := make([]byte, DecodedLen(len(enc)))
		nn, err := Decode(dst, []byte(enc))
		if err != nil {
			t.Fatalf("n=%d: Decode: %v", n, err)
		}
		if string(dst[:nn]) != string(src) {
			t.Fatalf("n=%d: Decode round-trip mismatch", n)
		}
	}
}

func TestDecodeError(t *testing.T) {
	// Invalid base64 must surface the stdlib error through both wrappers.
	if _, err := DecodeString("!!!!"); err == nil {
		t.Fatal("DecodeString: want error on invalid input, got nil")
	}
	dst := make([]byte, 8)
	if _, err := Decode(dst, []byte("!!!!")); err == nil {
		t.Fatal("Decode: want error on invalid input, got nil")
	}
}

// TestDecodeIdentical checks that Decode / DecodeString are byte- AND error-
// identical to encoding/base64.StdEncoding for a battery of valid, padded,
// invalid (incl. invalid byte deep inside a SIMD block), and whitespace inputs —
// the CorruptInputError offsets must match exactly, which is the whole point of
// bailing each non-clean block to the stdlib.
func TestDecodeIdentical(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	var inputs []string

	// Valid encodings of every length class (drives clean SSE/AVX2 blocks + tail).
	for n := 0; n <= 200; n++ {
		src := make([]byte, n)
		rng.Read(src)
		inputs = append(inputs, base64.StdEncoding.EncodeToString(src))
	}
	// Truncated / wrong-length quanta and stray padding.
	inputs = append(inputs,
		"", "=", "==", "===", "====", "A", "AB", "ABC", "ABCD", "ABCD=",
		"AAAA====", "A===", "AB==", "ABC=", "ABCDABC=", "AB=C",
	)
	// An invalid byte at many offsets, including deep inside what would be the
	// first/second SIMD block, so a wrong bail offset would diverge from stdlib.
	clean := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 96))
	for _, off := range []int{0, 1, 3, 7, 15, 16, 17, 31, 32, 33, 47, 60, 100} {
		if off < len(clean) {
			b := []byte(clean)
			b[off] = '!'
			inputs = append(inputs, string(b))
		}
	}
	// Whitespace (rejected by StdEncoding) at various offsets.
	for _, off := range []int{0, 4, 16, 20, 40} {
		if off < len(clean) {
			b := []byte(clean)
			b[off] = '\n'
			inputs = append(inputs, string(b))
		}
	}

	for _, in := range inputs {
		gotB, gotErr := DecodeString(in)
		wantB, wantErr := base64.StdEncoding.DecodeString(in)
		if !bytes.Equal(gotB, wantB) || !errEqual(gotErr, wantErr) {
			t.Fatalf("DecodeString(%q):\n got=%x err=%v\nwant=%x err=%v", in, gotB, gotErr, wantB, wantErr)
		}
		// Decode into a caller buffer must agree too (n and error).
		gd := make([]byte, DecodedLen(len(in)))
		wd := make([]byte, base64.StdEncoding.DecodedLen(len(in)))
		gn, ge := Decode(gd, []byte(in))
		wn, we := base64.StdEncoding.Decode(wd, []byte(in))
		if gn != wn || !errEqual(ge, we) || !bytes.Equal(gd[:gn], wd[:wn]) {
			t.Fatalf("Decode(%q):\n n=%d err=%v out=%x\nwant n=%d err=%v out=%x", in, gn, ge, gd[:gn], wn, we, wd[:wn])
		}
	}
}

func errEqual(a, b error) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	return a.Error() == b.Error()
}

func FuzzEncode(f *testing.F) {
	f.Add([]byte("hello world"))
	f.Fuzz(func(t *testing.T, src []byte) {
		if got, want := EncodeToString(src), base64.StdEncoding.EncodeToString(src); got != want {
			t.Fatalf("got=%q want=%q", got, want)
		}
	})
}

// FuzzDecode fuzzes Decode against encoding/base64: for arbitrary input bytes the
// decoded output AND the error (including CorruptInputError offset) must match the
// stdlib exactly — clean blocks go through the SIMD kernel, everything else bails.
func FuzzDecode(f *testing.F) {
	f.Add([]byte("aGVsbG8gd29ybGQ="))
	f.Add([]byte("AAAAAAAAAAAAAAAAAAAAAAAA"))
	f.Add([]byte("AAAA!AAAAAAAAAAAAAAAAAAA"))
	f.Add([]byte("AAAA\nAAAA"))
	f.Add([]byte("===="))
	f.Fuzz(func(t *testing.T, in []byte) {
		gotB, gotErr := DecodeString(string(in))
		wantB, wantErr := base64.StdEncoding.DecodeString(string(in))
		if !bytes.Equal(gotB, wantB) || !errEqual(gotErr, wantErr) {
			t.Fatalf("in=%q:\n got=%x err=%v\nwant=%x err=%v", in, gotB, gotErr, wantB, wantErr)
		}
		dst := make([]byte, DecodedLen(len(in)))
		gn, ge := Decode(dst, in)
		wd := make([]byte, base64.StdEncoding.DecodedLen(len(in)))
		wn, we := base64.StdEncoding.Decode(wd, in)
		if gn != wn || !errEqual(ge, we) || !bytes.Equal(dst[:gn], wd[:wn]) {
			t.Fatalf("Decode in=%q: n=%d/%d err=%v/%v", in, gn, wn, ge, we)
		}
	})
}

func benchData() []byte { b := make([]byte, 1<<20); rand.New(rand.NewSource(2)).Read(b); return b }

func benchEncoded() []byte { return []byte(base64.StdEncoding.EncodeToString(benchData())) }

func BenchmarkDecode(b *testing.B) {
	src := benchEncoded()
	dst := make([]byte, DecodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Decode(dst, src)
	}
}
func BenchmarkDecodeStdlib(b *testing.B) {
	src := benchEncoded()
	dst := make([]byte, base64.StdEncoding.DecodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		base64.StdEncoding.Decode(dst, src)
	}
}

// BenchmarkDecodeEmmansun decodes via github.com/emmansun/base64 (SIMD decode).
func BenchmarkDecodeEmmansun(b *testing.B) {
	src := benchEncoded()
	dst := make([]byte, emmansun.StdEncoding.DecodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		emmansun.StdEncoding.Decode(dst, src)
	}
}

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

// BenchmarkEncodeEmmansun benchmarks github.com/emmansun/base64, a pure-Go
// SIMD (amd64 SSE/AVX2/AVX512, arm64 NEON) drop-in for encoding/base64.
func BenchmarkEncodeEmmansun(b *testing.B) {
	src := benchData()
	dst := make([]byte, emmansun.StdEncoding.EncodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		emmansun.StdEncoding.Encode(dst, src)
	}
}

// BenchmarkEncodeCristalhq and the cristalhq differential cross-check live in
// base64_cristalhq_test.go (build-tagged !loong64, since cristalhq has no
// loong64 build).
