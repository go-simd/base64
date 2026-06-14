//go:build ignore

// Command gen produces encode_arm64_go127.s with go-asmgen: the Lemire/Muła
// vectorised base64 encode for arm64 NEON using the integer multiply that the
// Go arm64 assembler only gained upstream in Go 1.27 (VUMULL / VUMULL2 / VMUL).
//
// This is the multiply variant of the 6-bit-index extraction (the NEON analogue
// of the SSE PMULHUW/PMULLW trick in encode_amd64.s): per 12 input bytes
// (loaded as 16), a VTBL spread places [b1,b0,b2,b1] in each 32-bit lane, then
//
//	hi = (x & 0x0fc0fc00) *u16 0x04000040   // VUMULL/VUMULL2, then take high halfwords
//	lo = (x & 0x003f03f0) *u16 0x01000010   // VMUL on .H8 keeps the low halfword
//	idx = hi | lo                            // one 6-bit value per byte
//
// A 64-entry table lookup (the alphabet split across V8..V11, four VTBL source
// registers) then maps each index to its ASCII byte. The widening VUMULL gives a
// 32-bit product per 16-bit lane; a byte-shuffle VTBL (mask hiext) picks the two
// high bytes of each product, which is the 16-bit multiply-high the SSE PMULHUW
// does directly. The constants are identical to the SSE path's, and to the tail
// loop of github.com/emmansun/base64 (which emits the same instructions via WORD
// directives because it must build on released Go); here they are real mnemonics.
//
// Run: go run encode_arm64_go127_gen.go
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/arm64"
	"github.com/go-asmgen/asmgen/emit"
)

// rep tiles v to fill n bytes.
func rep(v []byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = v[i%len(v)]
	}
	return b
}

// alphabet is StdEncoding's A-Za-z0-9+/.
func alphabet() []byte {
	const s = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	return []byte(s)
}

func main() {
	f := emit.NewFile("arm64")

	// Spread mask: [b1,b0,b2,b1] per 32-bit lane (same as the SSE shuf).
	shuf := f.Data("shuf", []byte{1, 0, 2, 1, 4, 3, 5, 4, 7, 6, 8, 7, 10, 9, 11, 10})
	m1 := f.Data("mask1", rep([]byte{0x00, 0xfc, 0xc0, 0x0f}, 16)) // 0x0fc0fc00
	m2 := f.Data("mask2", rep([]byte{0xf0, 0x03, 0x3f, 0x00}, 16)) // 0x003f03f0
	// hiext picks the two high bytes of each 32-bit VUMULL product (V_lo holds
	// products of lanes 0..3, V_hi lanes 4..7); the byte indices walk the high
	// halfwords across the concatenated [V_lo, V_hi] pair.
	hiext := f.Data("hiext", []byte{2, 3, 6, 7, 10, 11, 14, 15, 18, 19, 22, 23, 26, 27, 30, 31})
	// 64-byte alphabet table, loaded into V8..V11 (four VTBL source regs).
	alpha := f.Data("alpha", alphabet())

	sig := abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		nil,
	)
	b := arm64.NewFunc("encodeBlocks", sig, 0)
	b.LoadArg("dst_base", "R0").
		LoadArg("src_base", "R1").
		LoadArg("n", "R2").
		// Constant tables.
		Raw("MOVD $%s(SB), R3", shuf).
		Raw("VLD1 (R3), [V3.B16]").
		Raw("MOVD $%s(SB), R3", m1).
		Raw("VLD1 (R3), [V4.B16]").
		Raw("MOVD $%s(SB), R3", m2).
		Raw("VLD1 (R3), [V5.B16]").
		Raw("MOVD $%s(SB), R3", hiext).
		Raw("VLD1 (R3), [V6.B16]").
		Raw("MOVD $%s(SB), R3", alpha).
		Raw("VLD1 (R3), [V8.B16, V9.B16, V10.B16, V11.B16]").
		// mullo = 0x01000010 per 32-bit lane; mulhi = mullo<<2 = 0x04000040.
		Raw("MOVD $0x01000010, R4").
		Raw("VDUP R4, V7.S4").
		Raw("VSHL $2, V7.S4, V12.S4").
		Raw("CBZ R2, done").
		Label("loop").
		Raw("VLD1 (R1), [V0.B16]").
		Raw("VTBL V3.B16, [V0.B16], V0.B16"). // spread to [b1,b0,b2,b1]
		// hi = (x & mask1) *u 0x04000040, take the high halfword of each product.
		Raw("VAND V4.B16, V0.B16, V2.B16").
		Raw("VUMULL V12.H4, V2.H4, V1.S4").           // products of lanes 0..3 -> V1
		Raw("VUMULL2 V12.H8, V2.H8, V2.S4").          // products of lanes 4..7 -> V2
		Raw("VTBL V6.B16, [V1.B16, V2.B16], V1.B16"). // pick high halfwords -> hi
		// lo = (x & mask2) *u 0x01000010, low halfword kept by VMUL on .H8.
		Raw("VAND V5.B16, V0.B16, V0.B16").
		Raw("VMUL V7.H8, V0.H8, V0.H8").
		Raw("VORR V0.B16, V1.B16, V0.B16"). // idx = hi | lo
		// idx (0..63) -> ASCII via the 64-entry alphabet table.
		Raw("VTBL V0.B16, [V8.B16, V9.B16, V10.B16, V11.B16], V0.B16").
		Raw("VST1 [V0.B16], (R0)").
		Raw("ADD $12, R1").
		Raw("ADD $16, R0").
		Raw("SUB $1, R2").
		Raw("CBNZ R2, loop").
		Label("done").
		Ret()
	f.Add(b.Func())

	// The VUMULL/VUMULL2/VMUL mnemonics only exist in the Go 1.27+ arm64
	// assembler, so the .s build constraint must match the go1.27 Go file.
	out := strings.Replace(f.String(), "//go:build arm64\n", "//go:build arm64 && go1.27\n", 1)

	if err := os.WriteFile("encode_arm64_go127.s", []byte(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote encode_arm64_go127.s")
}
