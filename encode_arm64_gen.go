//go:build ignore

// Command gen produces encode_arm64.s with go-asmgen: the aklomp/emmansun NEON
// base64 encode for arm64, the deinterleaving-I/O design that is the fast path on
// released Go.
//
// Per iteration it consumes 48 input bytes and emits 64 base64 chars:
//
//	VLD3.P 48(src), [V0,V1,V2]  // deinterleaving load: V0/V1/V2 are the three
//	                            //   byte-planes of the 16 24-bit groups, so each
//	                            //   lane i holds the 1st/2nd/3rd byte of group i.
//
// The four 6-bit indices of every 24-bit group are then extracted with plain
// shifts and inserts — no integer vector multiply, which is why this works on the
// released arm64 assembler (the same instructions emmansun uses on stable Go):
//
//	out0 =  b0 >> 2
//	out1 = (b0 << 4) | (b1 >> 4)   via VUSHR then VSLI
//	out2 = (b1 << 2) | (b2 >> 6)   via VUSHR then VSLI
//	out3 =  b2 & 0x3f
//	(out1/out2/out3 masked to 6 bits with VAND against a 0x3f splat)
//
// Four VTBL lookups map each plane's 0..63 indices through the 64-byte alphabet
// (held across V8..V11, four VTBL source registers), and a single interleaving
// store writes the result back in order:
//
//	VST4.P [V3,V4,V5,V6], 64(dst)
//
// VLD3.P/VST4.P deinterleave/interleave for free in the load/store unit, so the
// per-stream work is just three shifts, two inserts, three ANDs and four table
// lookups — the lever that lets this tie emmansun's ~22-23 GB/s on arm64, versus
// the ~6.6 GB/s of the old VTBL-spread + per-lane-shift kernel.
//
// Run: go run encode_arm64_gen.go
package main

import (
	"fmt"
	"os"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/arm64"
	"github.com/go-asmgen/asmgen/emit"
)

// alphabet returns the 64-byte VTBL lookup table: StdEncoding's A-Za-z0-9+/, or
// the URL-safe A-Za-z0-9-_ when url is set (the two variants differ only in the
// last two entries, indices 62/63).
func alphabet(url bool) []byte {
	const s = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	b := []byte(s)
	if url {
		b[62] = '-'
		b[63] = '_'
	}
	return b
}

type variant struct {
	suffix string
	url    bool
}

var variants = []variant{{"", false}, {"URL", true}}

func main() {
	f := emit.NewFile("arm64")

	sig := abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		nil,
	)
	for _, vr := range variants {
		genVariant(f, vr, sig)
	}

	if err := os.WriteFile("encode_arm64.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote encode_arm64.s")
}

func genVariant(f *emit.File, vr variant, sig abi.Signature) {
	// 64-byte alphabet table, loaded into V8..V11 (the four VTBL source regs).
	alpha := f.Data("alpha"+vr.suffix, alphabet(vr.url))

	b := arm64.NewFunc("encodeBlocks"+vr.suffix, sig, 0)
	b.LoadArg("dst_base", "R0").
		LoadArg("src_base", "R1").
		LoadArg("n", "R2").
		// Load the 64-byte alphabet into V8..V11 and splat the 6-bit mask 0x3f.
		Raw("MOVD $%s(SB), R3", alpha).
		Raw("VLD1 (R3), [V8.B16, V9.B16, V10.B16, V11.B16]").
		Raw("MOVD $0x3f, R4").
		Raw("VDUP R4, V7.B16").
		Raw("CBZ R2, done").
		Label("loop").
		// Deinterleaving load: V0/V1/V2 hold the three byte-planes of 16 groups.
		Raw("VLD3.P 48(R1), [V0.B16, V1.B16, V2.B16]").
		// Extract the four 6-bit indices per 24-bit group, multiply-free.
		Raw("VUSHR $2, V0.B16, V3.B16"). // out0 = b0>>2
		Raw("VUSHR $4, V1.B16, V4.B16"). // out1 hi = b1>>4
		Raw("VUSHR $6, V2.B16, V5.B16"). // out2 hi = b2>>6
		Raw("VSLI $4, V0.B16, V4.B16").  // out1 |= b0<<4
		Raw("VSLI $2, V1.B16, V5.B16").  // out2 |= b1<<2
		// Mask out1/out2/out3 to their low 6 bits (out0 is already <64).
		Raw("VAND V7.B16, V4.B16, V4.B16").
		Raw("VAND V7.B16, V5.B16, V5.B16").
		Raw("VAND V7.B16, V2.B16, V6.B16"). // out3 = b2&0x3f
		// Map each 0..63 index to its ASCII byte via the 64-entry table.
		Raw("VTBL V3.B16, [V8.B16, V9.B16, V10.B16, V11.B16], V3.B16").
		Raw("VTBL V4.B16, [V8.B16, V9.B16, V10.B16, V11.B16], V4.B16").
		Raw("VTBL V5.B16, [V8.B16, V9.B16, V10.B16, V11.B16], V5.B16").
		Raw("VTBL V6.B16, [V8.B16, V9.B16, V10.B16, V11.B16], V6.B16").
		// Interleaving store: write the 64 chars back in order.
		Raw("VST4.P [V3.B16, V4.B16, V5.B16, V6.B16], 64(R0)").
		Raw("SUB $1, R2").
		Raw("CBNZ R2, loop").
		Label("done").
		Ret()
	f.Add(b.Func())
}
