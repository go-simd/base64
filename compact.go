// Copyright (c) the go-simd/base64 authors
//
// SPDX-License-Identifier: BSD-3-Clause

package base64

import "unsafe"

// stdCompactLUT[c] is 0 for a standard base64 alphabet byte (A-Za-z0-9+/) and
// 0xFF for every other byte. Compact keeps exactly the bytes whose entry is 0.
// Because a valid entry is 0 and an invalid one is 0xFF, the OR of eight entries
// has a bit set in 0xC0 iff at least one of the eight bytes is not an alphabet
// byte — a single test that lets the hot loop move eight source bytes at a time
// whenever a window is entirely clean.
var stdCompactLUT = func() [256]byte {
	var t [256]byte
	for i := range t {
		t[i] = 0xFF
	}
	for _, c := range []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/") {
		t[c] = 0
	}
	return t
}()

// Compact copies into dst every standard base64 alphabet byte (A-Za-z0-9+/) of
// src, dropping whitespace, line breaks, padding and any other byte, and returns
// the number of bytes written. It is the SIMD-friendly "de-space" pre-pass a
// lenient base64 decoder runs before the vectorised decode: strip the non-
// alphabet bytes, then hand the packed alphabet run to Decode.
//
// It also applies the RFC-2045 / MRI relaxed padding rule so a lenient decoder
// can compact and terminate in one pass: a '=' that falls on a partial quantum
// (the running count of kept bytes mod 4 is 2 or 3) terminates the stream, so
// nothing from that '=' onward is copied; a '=' on a 4-byte boundary is dropped
// like any other stray byte.
//
// dst must have room for len(src) bytes (the compacted output is never longer
// than the input). Compact never allocates.
//
// The current kernel is a branch-free SWAR loop: eight bytes are classified with
// one OR-and-test and, when the whole window is valid, moved with a single 8-byte
// load/store; only windows straddling a stray byte fall back to the per-byte
// path. A wide-vector byte-compaction kernel (go-asmgen left-pack, per arch) is a
// planned drop-in replacement behind this same signature.
func Compact(dst, src []byte) int {
	return compact(dst, src)
}

// CompactString is Compact over a string source. It avoids copying the input to
// a []byte (base64 text is usually held as a string) by viewing the string's
// bytes read-only; the result is identical to Compact(dst, []byte(src)).
func CompactString(dst []byte, src string) int {
	if len(src) == 0 {
		return 0
	}
	return compact(dst, unsafe.Slice(unsafe.StringData(src), len(src)))
}

// compact is the shared []byte kernel behind Compact and CompactString.
func compact(dst, src []byte) int {
	n := len(src)
	m := 0
	i := 0
	for i+8 <= n {
		or := stdCompactLUT[src[i]] | stdCompactLUT[src[i+1]] |
			stdCompactLUT[src[i+2]] | stdCompactLUT[src[i+3]] |
			stdCompactLUT[src[i+4]] | stdCompactLUT[src[i+5]] |
			stdCompactLUT[src[i+6]] | stdCompactLUT[src[i+7]]
		if or&0xC0 == 0 {
			// Whole 8-byte window is alphabet: move it with one 8-byte load/store.
			// m <= i (kept count never exceeds bytes consumed) and i+8 <= n, so both
			// dst[m:m+8] and src[i:i+8] stay in bounds (dst is sized >= len(src)); the
			// leading index expressions are bounds-checked, the 8-byte copy is not.
			*(*uint64)(unsafe.Pointer(&dst[m])) = *(*uint64)(unsafe.Pointer(&src[i]))
			m += 8
			i += 8
			continue
		}
		// The window straddles at least one stray byte; fall back to per-byte.
		for k := 0; k < 8; k++ {
			c := src[i+k]
			if stdCompactLUT[c] == 0 {
				dst[m] = c
				m++
			} else if c == '=' && m&3 >= 2 {
				return m
			}
		}
		i += 8
	}
	for ; i < n; i++ {
		c := src[i]
		if stdCompactLUT[c] == 0 {
			dst[m] = c
			m++
		} else if c == '=' && m&3 >= 2 {
			return m
		}
	}
	return m
}
