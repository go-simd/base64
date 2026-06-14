//go:build ignore

// Command gen produces encode_arm64.s with go-asmgen: Lemire/Muła vectorised
// base64 encode for arm64 NEON. Per 12 input bytes (loaded as 16), a VTBL spread
// places one 24-bit group in each 32-bit lane; arm64 has no integer vector
// multiply, so the four 6-bit indices are pulled out with per-lane shifts and
// masks (VUSHR/VSHL/VAND/VORR) instead of the SSE PMULHUW/PMULLW trick; a VTBL
// offset-LUT then maps each index to its ASCII byte. The SSE range bucket
// (saturating-sub + signed-compare) is rebuilt from the ops the Go arm64
// assembler exposes — VUMIN for the saturating sub and VCMEQ for the >25 test —
// since VUQSUB/VCMHI are not available. Constant tables come from emit.File.Data;
// small splatted constants use VMOVI. Run: go run encode_arm64_gen.go
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/arm64"
	"github.com/go-asmgen/asmgen/emit"
)

// rep4 repeats a little-endian 32-bit constant to fill 16 bytes (one value per
// 32-bit lane), matching how the per-lane VAND masks are applied.
func rep4(v uint32) []byte {
	b := make([]byte, 16)
	for i := 0; i < 4; i++ {
		b[i*4+0] = byte(v)
		b[i*4+1] = byte(v >> 8)
		b[i*4+2] = byte(v >> 16)
		b[i*4+3] = byte(v >> 24)
	}
	return b
}

func main() {
	f := emit.NewFile("arm64")
	shuf := f.Data("shuf", []byte{1, 0, 2, 1, 4, 3, 5, 4, 7, 6, 8, 7, 10, 9, 11, 10})
	// Per-lane masks selecting each index field after the shift that aligns it.
	m0 := f.Data("m0", rep4(0x0000003f)) // i0 -> byte0
	m1a := f.Data("m1a", rep4(0x00003000))
	m1b := f.Data("m1b", rep4(0x00000f00)) // i1 -> byte1
	m2a := f.Data("m2a", rep4(0x003c0000))
	m2b := f.Data("m2b", rep4(0x00030000)) // i2 -> byte2
	m3 := f.Data("m3", rep4(0x3f000000))   // i3 -> byte3
	lut := f.Data("lut", []byte{65, 71, 252, 252, 252, 252, 252, 252, 252, 252, 252, 252, 237, 240, 0, 0})

	sig := abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		nil,
	)
	b := arm64.NewFunc("encodeBlocks", sig, 0)
	b.LoadArg("dst_base", "R0").
		LoadArg("src_base", "R1").
		LoadArg("n", "R2").
		// Load constant tables once.
		Raw("MOVD $%s(SB), R3", shuf).
		Raw("VLD1 (R3), [V7.B16]").
		Raw("MOVD $%s(SB), R3", m0).
		Raw("VLD1 (R3), [V8.B16]").
		Raw("MOVD $%s(SB), R3", m1a).
		Raw("VLD1 (R3), [V9.B16]").
		Raw("MOVD $%s(SB), R3", m1b).
		Raw("VLD1 (R3), [V10.B16]").
		Raw("MOVD $%s(SB), R3", m2a).
		Raw("VLD1 (R3), [V11.B16]").
		Raw("MOVD $%s(SB), R3", m2b).
		Raw("VLD1 (R3), [V12.B16]").
		Raw("MOVD $%s(SB), R3", m3).
		Raw("VLD1 (R3), [V13.B16]").
		Raw("MOVD $%s(SB), R3", lut).
		Raw("VLD1 (R3), [V14.B16]").
		// Splatted scalar constants.
		Raw("VMOVI $51, V15.B16").
		Raw("VMOVI $25, V16.B16").
		Raw("VMOVI $1, V17.B16").
		Raw("VMOVI $0, V18.B16").
		Raw("CBZ R2, done").
		Label("loop").
		// Load 16 bytes (use first 12), spread to 4 lanes of [b1,b0,b2,b1].
		Raw("VLD1 (R1), [V0.B16]").
		Raw("VTBL V7.B16, [V0.B16], V0.B16").
		// Extract the four 6-bit indices via per-32-bit-lane shifts + masks.
		// i0 = b0>>2          -> byte0
		Raw("VUSHR $10, V0.S4, V1.S4").
		Raw("VAND V8.B16, V1.B16, V1.B16").
		// i1 = ((b0&3)<<4)|(b1>>4) -> byte1
		Raw("VSHL $4, V0.S4, V2.S4").
		Raw("VAND V9.B16, V2.B16, V3.B16").
		Raw("VAND V10.B16, V2.B16, V2.B16").
		Raw("VORR V3.B16, V2.B16, V2.B16").
		Raw("VORR V2.B16, V1.B16, V1.B16").
		// i2 = ((b1&15)<<2)|(b2>>6) -> byte2
		Raw("VSHL $18, V0.S4, V2.S4").
		Raw("VAND V11.B16, V2.B16, V2.B16").
		Raw("VUSHR $6, V0.S4, V3.S4").
		Raw("VAND V12.B16, V3.B16, V3.B16").
		Raw("VORR V3.B16, V2.B16, V2.B16").
		Raw("VORR V2.B16, V1.B16, V1.B16").
		// i3 = b2&63          -> byte3
		Raw("VSHL $8, V0.S4, V2.S4").
		Raw("VAND V13.B16, V2.B16, V2.B16").
		Raw("VORR V2.B16, V1.B16, V1.B16"). // V1 = packed indices (one 6-bit value per byte)
		// Range bucket: bucket = (idx - min(idx,51)) + (idx>25 ? 1 : 0).
		Raw("VUMIN V15.B16, V1.B16, V2.B16"). // min(idx,51)
		Raw("VSUB V2.B16, V1.B16, V2.B16").   // sat = idx - min(idx,51)
		Raw("VUMIN V16.B16, V1.B16, V3.B16"). // min(idx,25)
		Raw("VSUB V3.B16, V1.B16, V3.B16").   // diff = idx - min(idx,25)
		Raw("VCMEQ V18.B16, V3.B16, V3.B16"). // 0xff where idx<=25
		// addend = (^mask)&1 = 1 where idx>25; build via (mask^1)&1.
		Raw("VEOR V17.B16, V3.B16, V3.B16").
		Raw("VAND V17.B16, V3.B16, V3.B16").
		Raw("VADD V3.B16, V2.B16, V2.B16"). // bucket
		// offsets = lut[bucket]; ascii = idx + offsets.
		Raw("VTBL V2.B16, [V14.B16], V2.B16").
		Raw("VADD V1.B16, V2.B16, V2.B16").
		Raw("VST1 [V2.B16], (R0)").
		Raw("ADD $12, R1").
		Raw("ADD $16, R0").
		Raw("SUB $1, R2").
		Raw("CBNZ R2, loop").
		Label("done").
		Ret()
	f.Add(b.Func())
	// This shift-based kernel is the stable-Go path; the go1.27 build uses the
	// VUMULL multiply kernel instead (encode_arm64_go127.s), so this .s is gated
	// off there to avoid a duplicate ·encodeBlocks symbol.
	out := strings.Replace(f.String(), "//go:build arm64\n", "//go:build arm64 && !go1.27\n", 1)
	if err := os.WriteFile("encode_arm64.s", []byte(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote encode_arm64.s")
}
