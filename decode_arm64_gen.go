//go:build ignore

// Command gen produces decode_arm64.s with go-asmgen: the aklomp/emmansun NEON
// base64 *decode* for arm64, the high-throughput deinterleaving-I/O design that
// sustains ~14.6 GB/s on released Go — ~3x the stdlib and ~1.7x the old
// Lemire/Muła 16-char kernel (~8.5 GB/s), within ~0.9x of the emmansun reference
// (which it does correctly where emmansun mis-accepts >=0x80 bytes).
//
// Per iteration it consumes 64 base64 chars and emits 48 bytes:
//
//	VLD4.P 64(src), [V20,V21,V22,V23]  // deinterleaving load: V20/V21/V22/V23
//	                                   //   are the four char-planes of the 16
//	                                   //   4-char groups (lane i = the k-th char
//	                                   //   of group i).
//
// Each plane is translated char->6-bit value with a TWO-table lookup that also
// flags invalid bytes: a VTBL through the first 64 LUT entries (V8..V11) handles
// chars 0x00..0x3F, and a VTBX through the next 64 entries (V12..V15), after
// subtracting 0x40, fills in chars 0x40..0x7F while leaving the first lookup in
// place where it already hit. Invalid chars (whitespace, padding '=', any
// non-alphabet byte, and bytes >= 0x80) map to 0xFF, i.e. a value >= 0x40, which
// VCMHS against 0x40 detects; VUMAXV reduces the four invalid masks to one GPR
// and the kernel returns the number of 64-char blocks decoded so far on any
// invalid byte (so the caller re-decodes the remainder — including the padded
// final quantum — with encoding/base64, keeping errors and offsets identical).
//
// The 4x6-bit -> 3-byte pack is pure shifts (no compaction shuffle needed):
//
//	o0 = (v0 << 2) | (v1 >> 4)
//	o1 = (v1 << 4) | (v2 >> 2)
//	o2 = (v2 << 6) |  v3
//
// and a single interleaving store VST3.P writes the 48 bytes back in order.
//
// VTBX, VCMHS and VUMAXV are not recognised by the released arm64 assembler, so
// they are emitted as raw WORD encodings (the same ones emmansun uses).
//
// Run: go run decode_arm64_gen.go
package main

import (
	"fmt"
	"os"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/arm64"
	"github.com/go-asmgen/asmgen/emit"
)

// stdDecodeLUT[c] = the 6-bit value of base64 (StdEncoding) char c, or 0xFF for
// any non-alphabet byte. Indices 0x80..0xFF are implicitly invalid (the kernel
// only loads the low 128 entries; bytes >= 0x80 fail the VCMHS >= 0x40 test
// because neither VTBL nor VTBX writes them, leaving the VTBL's 0xFF default).
var stdDecodeLUT = [128]byte{
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 62, 255, 255, 255, 63,
	52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 255, 255, 255, 255, 255, 255,
	255, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14,
	15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 255, 255, 255, 255, 255,
	255, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40,
	41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 255, 255, 255, 255, 255,
}

func main() {
	f := emit.NewFile("arm64")

	lutLo := f.Data("darmLutLo", stdDecodeLUT[:64]) // V8..V11: chars 0x00..0x3F
	lutHi := f.Data("darmLutHi", stdDecodeLUT[64:]) // V12..V15: chars 0x40..0x7F

	sig := abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		[]abi.Arg{abi.Scalar("ret", abi.Int64)},
	)

	b := arm64.NewFunc("decodeBlocks", sig, 0)
	b.LoadArg("dst_base", "R0").
		LoadArg("src_base", "R1").
		LoadArg("n", "R2").
		// Load the two 64-byte translate tables into V8..V11 and V12..V15.
		Raw("MOVD $%s(SB), R3", lutLo).
		Raw("VLD1 (R3), [V8.B16, V9.B16, V10.B16, V11.B16]").
		Raw("MOVD $%s(SB), R3", lutHi).
		Raw("VLD1 (R3), [V12.B16, V13.B16, V14.B16, V15.B16]").
		Raw("MOVD $0x40, R4").Raw("VDUP R4, V7.B16"). // 0x40 splat (subtract + bound)
		Raw("VMOVI $0x80, V28.B16").                  // high-bit mask (>=0x80 is invalid)
		Raw("MOVD $0, R5").                           // R5 = block counter
		Raw("CBZ R2, done").
		Label("loop").
		// Deinterleaving load: V20..V23 = the four char-planes of 16 groups.
		Raw("VLD4.P 64(R1), [V20.B16, V21.B16, V22.B16, V23.B16]").
		// Detect high-bit (>=0x80) bytes, which the two 64-entry tables alone
		// cannot flag (an out-of-range VTBL index just yields 0). Without this a
		// byte like 0xCF would decode to 0 silently — the same correctness hole
		// emmansun's kernel has; go-simd must match the stdlib's rejection
		// exactly. We only need block-level "any high bit" (the scalar tail
		// re-finds the exact offset), so OR the four char-planes, then isolate
		// the high bit once into V24.
		Raw("VORR V21.B16, V20.B16, V24.B16").
		Raw("VORR V23.B16, V22.B16, V25.B16").
		Raw("VORR V25.B16, V24.B16, V24.B16").
		Raw("VAND V28.B16, V24.B16, V24.B16"). // V24 = OR-of-high-bits across the block
		// First-table lookup (chars 0x00..0x3F).
		Raw("VTBL V20.B16, [V8.B16, V9.B16, V10.B16, V11.B16], V0.B16").
		Raw("VTBL V21.B16, [V8.B16, V9.B16, V10.B16, V11.B16], V1.B16").
		Raw("VTBL V22.B16, [V8.B16, V9.B16, V10.B16, V11.B16], V2.B16").
		Raw("VTBL V23.B16, [V8.B16, V9.B16, V10.B16, V11.B16], V3.B16").
		// Second-table extend (chars 0x40..0x7F): subtract 0x40, VTBX keeps the
		// first lookup where it already matched.
		Raw("VSUB V7.B16, V20.B16, V20.B16").
		Raw("WORD $0x4e147180"). // VTBX V20.B16, [V12.B16,V13.B16,V14.B16,V15.B16], V0.B16
		Raw("VSUB V7.B16, V21.B16, V21.B16").
		Raw("WORD $0x4e157181"). // VTBX V21.B16, [...], V1.B16
		Raw("VSUB V7.B16, V22.B16, V22.B16").
		Raw("WORD $0x4e167182"). // VTBX V22.B16, [...], V2.B16
		Raw("VSUB V7.B16, V23.B16, V23.B16").
		Raw("WORD $0x4e177183"). // VTBX V23.B16, [...], V3.B16
		// Validity: any value >= 0x40 is invalid. VCMHS V7, Vk -> 0xFF where Vk>=0x40.
		Raw("WORD $0x6e273c10"). // VCMHS V7.B16, V0.B16, V16.B16
		Raw("WORD $0x6e273c31"). // VCMHS V7.B16, V1.B16, V17.B16
		Raw("WORD $0x6e273c52"). // VCMHS V7.B16, V2.B16, V18.B16
		Raw("WORD $0x6e273c73"). // VCMHS V7.B16, V3.B16, V19.B16
		Raw("VORR V17.B16, V16.B16, V16.B16").
		Raw("VORR V18.B16, V16.B16, V16.B16").
		Raw("VORR V19.B16, V16.B16, V16.B16").
		// Fold in the combined high-bit (>=0x80) mask.
		Raw("VORR V24.B16, V16.B16, V16.B16").
		// Reduce: VUMAXV -> max byte; nonzero means some invalid byte -> stop.
		Raw("WORD $0x6e30aa11"). // VUMAXV V16.B16, V17
		Raw("VMOV V17.B[0], R6").
		Raw("CBNZ R6, done").
		// Pack 4x6-bit -> 3 bytes (pure shifts).
		Raw("VSHL $2, V0.B16, V4.B16").
		Raw("VUSHR $4, V1.B16, V16.B16").
		Raw("VORR V16.B16, V4.B16, V4.B16"). // o0 = (v0<<2)|(v1>>4)
		Raw("VSHL $4, V1.B16, V5.B16").
		Raw("VUSHR $2, V2.B16, V16.B16").
		Raw("VORR V16.B16, V5.B16, V5.B16"). // o1 = (v1<<4)|(v2>>2)
		Raw("VSHL $6, V2.B16, V16.B16").
		Raw("VORR V16.B16, V3.B16, V6.B16"). // o2 = (v2<<6)|v3
		// Interleaving store: 48 bytes in order.
		Raw("VST3.P [V4.B16, V5.B16, V6.B16], 48(R0)").
		Raw("ADD $1, R5").
		Raw("SUB $1, R2").
		Raw("CBNZ R2, loop").
		Label("done").
		Raw("MOVD R5, ret+56(FP)").
		Ret()
	f.Add(b.Func())

	if err := os.WriteFile("decode_arm64.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote decode_arm64.s")
}
