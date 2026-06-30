package base64

import (
	"bytes"
	"encoding/base64"
	"math/rand"
	"testing"

	emmansun "github.com/emmansun/base64"
)

// encPair binds one of this package's encodings to the encoding/base64 oracle it
// must match byte- and error-for-byte. The four mirror the stdlib's four exactly.
type encPair struct {
	name string
	enc  *Encoding
	ref  *base64.Encoding
}

func encodings() []encPair {
	return []encPair{
		{"Std", StdEncoding, base64.StdEncoding},
		{"URL", URLEncoding, base64.URLEncoding},
		{"RawStd", RawStdEncoding, base64.RawStdEncoding},
		{"RawURL", RawURLEncoding, base64.RawURLEncoding},
	}
}

func TestEncode(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	// n=0..512 covers every length class through several full SIMD blocks plus a
	// scattering of larger sizes.
	sizes := make([]int, 0, 520)
	for n := 0; n <= 512; n++ {
		sizes = append(sizes, n)
	}
	sizes = append(sizes, 1000, 4096)
	for _, ep := range encodings() {
		for _, n := range sizes {
			src := make([]byte, n)
			rng.Read(src)
			if got, want := ep.enc.EncodeToString(src), ep.ref.EncodeToString(src); got != want {
				t.Fatalf("%s n=%d:\n got=%q\nwant=%q", ep.name, n, got, want)
			}
		}
	}
	// Sanity-check the benchmark competitor is byte-identical to StdEncoding so the
	// Performance table compares like for like. cristalhq has no loong64 build (its
	// arch tag lists predate loong64), so its differential cross-check lives in a
	// build-tagged helper that is a no-op there. See base64_cristalhq_test.go.
	for _, n := range []int{0, 1, 2, 3, 47, 48, 49, 4096} {
		src := make([]byte, n)
		rng.Read(src)
		if got, want := emmansun.StdEncoding.EncodeToString(src), base64.StdEncoding.EncodeToString(src); got != want {
			t.Fatalf("emmansun n=%d:\n got=%q\nwant=%q", n, got, want)
		}
		checkCristalhq(t, n, src)
	}
}

func TestDecode(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for _, ep := range encodings() {
		for n := 0; n <= 512; n++ {
			src := make([]byte, n)
			rng.Read(src)
			enc := ep.enc.EncodeToString(src)

			// DecodedLen / DecodeString.
			if got := ep.enc.DecodedLen(len(enc)); got < n {
				t.Fatalf("%s n=%d: DecodedLen=%d < %d", ep.name, n, got, n)
			}
			got, err := ep.enc.DecodeString(enc)
			if err != nil {
				t.Fatalf("%s n=%d: DecodeString: %v", ep.name, n, err)
			}
			if string(got) != string(src) {
				t.Fatalf("%s n=%d: DecodeString round-trip mismatch", ep.name, n)
			}

			// Decode into a caller-supplied buffer.
			dst := make([]byte, ep.enc.DecodedLen(len(enc)))
			nn, err := ep.enc.Decode(dst, []byte(enc))
			if err != nil {
				t.Fatalf("%s n=%d: Decode: %v", ep.name, n, err)
			}
			if string(dst[:nn]) != string(src) {
				t.Fatalf("%s n=%d: Decode round-trip mismatch", ep.name, n)
			}
		}
	}
}

// TestPackageHelpers exercises the package-level StdEncoding shortcuts (kept for
// backwards compatibility with the original API), confirming they match the
// StdEncoding methods and encoding/base64.StdEncoding exactly.
func TestPackageHelpers(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for _, n := range []int{0, 1, 2, 3, 47, 48, 49, 200, 1000} {
		src := make([]byte, n)
		rng.Read(src)
		if EncodedLen(n) != base64.StdEncoding.EncodedLen(n) {
			t.Fatalf("EncodedLen(%d) mismatch", n)
		}
		// EncodeToString and Encode-into-buffer.
		got := EncodeToString(src)
		if want := base64.StdEncoding.EncodeToString(src); got != want {
			t.Fatalf("EncodeToString n=%d: got=%q want=%q", n, got, want)
		}
		dst := make([]byte, EncodedLen(n))
		Encode(dst, src)
		if string(dst) != got {
			t.Fatalf("Encode n=%d: buffer != EncodeToString", n)
		}
		// DecodedLen / DecodeString / Decode.
		if DecodedLen(len(got)) != base64.StdEncoding.DecodedLen(len(got)) {
			t.Fatalf("DecodedLen mismatch n=%d", n)
		}
		db, err := DecodeString(got)
		if err != nil || string(db) != string(src) {
			t.Fatalf("DecodeString n=%d: err=%v roundtrip mismatch", n, err)
		}
		buf := make([]byte, DecodedLen(len(got)))
		nn, err := Decode(buf, []byte(got))
		if err != nil || string(buf[:nn]) != string(src) {
			t.Fatalf("Decode n=%d: err=%v roundtrip mismatch", n, err)
		}
	}
}

func TestDecodeError(t *testing.T) {
	for _, ep := range encodings() {
		// Invalid base64 must surface the stdlib error through both wrappers.
		if _, err := ep.enc.DecodeString("!!!!"); err == nil {
			t.Fatalf("%s DecodeString: want error on invalid input, got nil", ep.name)
		}
		dst := make([]byte, 8)
		if _, err := ep.enc.Decode(dst, []byte("!!!!")); err == nil {
			t.Fatalf("%s Decode: want error on invalid input, got nil", ep.name)
		}
		// A char from the *other* alphabet must be rejected exactly like the stdlib
		// (Std rejects '-'/'_'; URL rejects '+'/'/'), proving the alphabet is wired
		// through to the SIMD validate LUTs, not just the scalar tail.
		alien := "----____----____" // 16 chars: all '-'/'_'
		gotB, gotErr := ep.enc.DecodeString(alien)
		wantB, wantErr := ep.ref.DecodeString(alien)
		if !bytes.Equal(gotB, wantB) || !errEqual(gotErr, wantErr) {
			t.Fatalf("%s DecodeString(%q):\n got=%x err=%v\nwant=%x err=%v", ep.name, alien, gotB, gotErr, wantB, wantErr)
		}
	}
}

// TestDecodeIdentical checks that Decode / DecodeString are byte- AND error-
// identical to encoding/base64.StdEncoding for a battery of valid, padded,
// invalid (incl. invalid byte deep inside a SIMD block), and whitespace inputs —
// the CorruptInputError offsets must match exactly, which is the whole point of
// bailing each non-clean block to the stdlib.
func TestDecodeIdentical(t *testing.T) {
	for _, ep := range encodings() {
		rng := rand.New(rand.NewSource(11))
		var inputs []string

		// Valid encodings of every length class (drives clean SSE/AVX2 blocks + tail).
		for n := 0; n <= 200; n++ {
			src := make([]byte, n)
			rng.Read(src)
			inputs = append(inputs, ep.ref.EncodeToString(src))
		}
		// Truncated / wrong-length quanta and stray padding.
		inputs = append(inputs,
			"", "=", "==", "===", "====", "A", "AB", "ABC", "ABCD", "ABCD=",
			"AAAA====", "A===", "AB==", "ABC=", "ABCDABC=", "AB=C",
		)
		// Chars from the *other* alphabet at SIMD-block-interior offsets, so a kernel
		// that accepted the wrong alphabet (or bailed at the wrong offset) would
		// diverge from the stdlib's CorruptInputError.
		cleanA := ep.ref.EncodeToString(bytes.Repeat([]byte{0x5a}, 96))
		for _, alien := range []byte{'+', '/', '-', '_'} {
			for _, off := range []int{0, 1, 16, 17, 32, 47, 64} {
				if off < len(cleanA) {
					b := []byte(cleanA)
					b[off] = alien
					inputs = append(inputs, string(b))
				}
			}
		}
		// An invalid byte at many offsets, including deep inside what would be the
		// first/second SIMD block, so a wrong bail offset would diverge from stdlib.
		clean := ep.ref.EncodeToString(bytes.Repeat([]byte{0x5a}, 96))
		for _, off := range []int{0, 1, 3, 7, 15, 16, 17, 31, 32, 33, 47, 60, 100} {
			if off < len(clean) {
				b := []byte(clean)
				b[off] = '!'
				inputs = append(inputs, string(b))
			}
		}
		// Whitespace (rejected) at various offsets.
		for _, off := range []int{0, 4, 16, 20, 40} {
			if off < len(clean) {
				b := []byte(clean)
				b[off] = '\n'
				inputs = append(inputs, string(b))
			}
		}

		for _, in := range inputs {
			gotB, gotErr := ep.enc.DecodeString(in)
			wantB, wantErr := ep.ref.DecodeString(in)
			if !bytes.Equal(gotB, wantB) || !errEqual(gotErr, wantErr) {
				t.Fatalf("%s DecodeString(%q):\n got=%x err=%v\nwant=%x err=%v", ep.name, in, gotB, gotErr, wantB, wantErr)
			}
			// Decode into a caller buffer must agree too (n and error).
			gd := make([]byte, ep.enc.DecodedLen(len(in)))
			wd := make([]byte, ep.ref.DecodedLen(len(in)))
			gn, ge := ep.enc.Decode(gd, []byte(in))
			wn, we := ep.ref.Decode(wd, []byte(in))
			if gn != wn || !errEqual(ge, we) || !bytes.Equal(gd[:gn], wd[:wn]) {
				t.Fatalf("%s Decode(%q):\n n=%d err=%v out=%x\nwant n=%d err=%v out=%x", ep.name, in, gn, ge, gd[:gn], wn, we, wd[:wn])
			}
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
		for _, ep := range encodings() {
			if got, want := ep.enc.EncodeToString(src), ep.ref.EncodeToString(src); got != want {
				t.Fatalf("%s got=%q want=%q", ep.name, got, want)
			}
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
	f.Add([]byte("aGVsbG8-d29ybGQ_"))
	f.Fuzz(func(t *testing.T, in []byte) {
		for _, ep := range encodings() {
			gotB, gotErr := ep.enc.DecodeString(string(in))
			wantB, wantErr := ep.ref.DecodeString(string(in))
			if !bytes.Equal(gotB, wantB) || !errEqual(gotErr, wantErr) {
				t.Fatalf("%s in=%q:\n got=%x err=%v\nwant=%x err=%v", ep.name, in, gotB, gotErr, wantB, wantErr)
			}
			dst := make([]byte, ep.enc.DecodedLen(len(in)))
			gn, ge := ep.enc.Decode(dst, in)
			wd := make([]byte, ep.ref.DecodedLen(len(in)))
			wn, we := ep.ref.Decode(wd, in)
			if gn != wn || !errEqual(ge, we) || !bytes.Equal(dst[:gn], wd[:wn]) {
				t.Fatalf("%s Decode in=%q: n=%d/%d err=%v/%v", ep.name, in, gn, wn, ge, we)
			}
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

// URL-safe benchmarks confirm the -_ kernels run at full SIMD speed (the alphabet
// is a table swap, so they match the +/ throughput and never pessimize vs stdlib).

func benchURLEncoded() []byte { return []byte(base64.URLEncoding.EncodeToString(benchData())) }

func BenchmarkEncodeURL(b *testing.B) {
	src := benchData()
	dst := make([]byte, URLEncoding.EncodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		URLEncoding.Encode(dst, src)
	}
}
func BenchmarkEncodeURLStdlib(b *testing.B) {
	src := benchData()
	dst := make([]byte, base64.URLEncoding.EncodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		base64.URLEncoding.Encode(dst, src)
	}
}
func BenchmarkDecodeURL(b *testing.B) {
	src := benchURLEncoded()
	dst := make([]byte, URLEncoding.DecodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		URLEncoding.Decode(dst, src)
	}
}
func BenchmarkDecodeURLStdlib(b *testing.B) {
	src := benchURLEncoded()
	dst := make([]byte, base64.URLEncoding.DecodedLen(len(src)))
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		base64.URLEncoding.Decode(dst, src)
	}
}

// BenchmarkEncodeCristalhq and the cristalhq differential cross-check live in
// base64_cristalhq_test.go (build-tagged !loong64, since cristalhq has no
// loong64 build).
