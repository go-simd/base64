// Copyright (c) the go-simd/base64 authors
//
// SPDX-License-Identifier: BSD-3-Clause

package base64

import (
	"bytes"
	"encoding/base64"
	"math/rand"
	"testing"
)

// refCompact is the obvious byte-at-a-time reference the SWAR compact() must
// match: keep every standard-alphabet byte, drop everything else, and stop at a
// '=' that lands on a partial quantum (kept-count mod 4 is 2 or 3).
func refCompact(src []byte) []byte {
	out := make([]byte, 0, len(src))
	for _, c := range src {
		if stdCompactLUT[c] == 0 {
			out = append(out, c)
		} else if c == '=' && len(out)&3 >= 2 {
			return out
		}
	}
	return out
}

// TestCompact checks Compact and CompactString against refCompact over both
// crafted edge cases (every branch of the SWAR loop) and a large random sweep of
// lengths, so the 8-byte fast path, the per-byte dirty windows, the sub-8 tail
// and the '=' stop are all exercised on both the []byte and string entry points.
func TestCompact(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(""),
		[]byte("A"),
		[]byte("ABCDEFG"),                 // 7 bytes: tail only, all valid
		[]byte("ABCDEFGH"),                // 8 bytes: one clean fast window
		[]byte("ABCDEFGHIJKLMNOP"),        // two clean windows
		[]byte("ABCDEFGHIJKLMNOPQ"),       // two windows + 1 tail
		[]byte("aGVsbG8gd29ybGQ=\n"),      // padding + newline
		[]byte("aGVs bG8=\n junk"),        // spaces, newline, junk
		[]byte("AB=CDEFGH"),               // '=' on partial quad inside a window -> stop
		[]byte("ABCD=EFGH"),               // '=' on quad boundary inside a window -> drop
		[]byte("AB="),                     // '=' partial quad in the tail -> stop
		[]byte("ABCD="),                   // '=' quad boundary in the tail -> drop
		[]byte("===="),                    // only padding -> empty
		[]byte("\n\r\t   \n\r\t  "),       // only whitespace -> empty
		[]byte{'A', 'B', 0xFF, 0x80, 'C'}, // high bytes dropped
		bytes.Repeat([]byte("YWJj\n"), 40),
	}
	check := func(t *testing.T, src []byte) {
		want := refCompact(src)
		dst := make([]byte, len(src))
		n := Compact(dst, src)
		if !bytes.Equal(dst[:n], want) {
			t.Fatalf("Compact(%q) = %q, want %q", src, dst[:n], want)
		}
		ds := make([]byte, len(src))
		ns := CompactString(ds, string(src))
		if !bytes.Equal(ds[:ns], want) {
			t.Fatalf("CompactString(%q) = %q, want %q", src, ds[:ns], want)
		}
	}
	for _, c := range cases {
		check(t, c)
	}

	// Random sweep: mix alphabet bytes with newlines, spaces, '=' and high bytes
	// across every length class through several full windows.
	rng := rand.New(rand.NewSource(7))
	alpha := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/")
	for n := 0; n <= 300; n++ {
		src := make([]byte, n)
		for i := range src {
			switch rng.Intn(10) {
			case 0:
				src[i] = "\n\r\t ="[rng.Intn(5)]
			case 1:
				src[i] = byte(0x80 + rng.Intn(0x80))
			default:
				src[i] = alpha[rng.Intn(len(alpha))]
			}
		}
		check(t, src)
	}
}

// TestCompactThenDecode confirms the intended pipeline — Compact a newline-
// wrapped, padded encoding then Decode the packed run — reproduces the payload,
// which is the exact use go-ruby-base64's lenient Decode64 makes of Compact.
func TestCompactThenDecode(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	// Full-quad payloads (n % 3 == 0) so the wrapped encoding is pure quads with
	// no '=' padding; the '='-stop and 2/3-char tail are covered in TestCompact
	// and in go-ruby-base64's oracle respectively.
	for n := 0; n <= 400; n += 3 {
		raw := make([]byte, n)
		rng.Read(raw)
		// Wrap the standard encoding at 60 columns, MRI encode64 style.
		enc := base64.StdEncoding.EncodeToString(raw)
		var wrapped []byte
		for i := 0; i < len(enc); i += 60 {
			end := i + 60
			if end > len(enc) {
				end = len(enc)
			}
			wrapped = append(wrapped, enc[i:end]...)
			wrapped = append(wrapped, '\n')
		}
		buf := make([]byte, len(wrapped))
		m := CompactString(buf, string(wrapped))
		full := m - m%4 // '=' padding was stripped; only whole quads remain here
		got := make([]byte, base64.StdEncoding.DecodedLen(full))
		dn, err := Decode(got, buf[:full])
		if err != nil {
			t.Fatalf("n=%d Decode: %v", n, err)
		}
		if !bytes.Equal(got[:dn], raw) {
			t.Fatalf("n=%d roundtrip mismatch: got %d want %d bytes", n, dn, len(raw))
		}
	}
}

// TestDecodeInPlace pins the in-place decode guarantee documented on Decode: dst
// and src sharing one backing array (dst a prefix of src) yields the same result
// as decoding into a separate buffer. go-ruby-base64 relies on this to decode a
// de-spaced buffer without a second allocation.
func TestDecodeInPlace(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	for n := 0; n <= 512; n++ {
		raw := make([]byte, n)
		rng.Read(raw)
		enc := []byte(RawStdEncoding.EncodeToString(raw)) // no padding: pure quads + tail
		// Reference decode into a fresh buffer.
		want := make([]byte, RawStdEncoding.DecodedLen(len(enc)))
		wn, err := RawStdEncoding.Decode(want, enc)
		if err != nil {
			t.Fatalf("n=%d ref decode: %v", n, err)
		}
		// In-place: decode enc into enc (dst == src).
		gn, err := RawStdEncoding.Decode(enc, enc)
		if err != nil {
			t.Fatalf("n=%d in-place decode: %v", n, err)
		}
		if !bytes.Equal(enc[:gn], want[:wn]) {
			t.Fatalf("n=%d in-place decode mismatch", n)
		}
	}
}

// BenchmarkCompact reports the de-space throughput on newline-wrapped input (the
// lenient-decode hot path): a 3 KiB payload encoded and wrapped at 60 columns.
func BenchmarkCompact(b *testing.B) {
	raw := make([]byte, 3072)
	for i := range raw {
		raw[i] = byte(i % 256)
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	var src []byte
	for i := 0; i < len(enc); i += 60 {
		end := i + 60
		if end > len(enc) {
			end = len(enc)
		}
		src = append(src, enc[i:end]...)
		src = append(src, '\n')
	}
	dst := make([]byte, len(src))
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		_ = Compact(dst, src)
	}
}
