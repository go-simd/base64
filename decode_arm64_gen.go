//go:build ignore

// Command gen produces decode_arm64.s with go-asmgen: Lemire/Muła vectorised
// base64 *decode* for arm64 NEON. Unlike base64 *encode* (whose pack needs an
// integer multiply arm64's released-Go NEON lacks), decode's pack is a pure
// shift/mask gather, so arm64 gets a real SIMD decode kernel.
//
// Per 16 ASCII chars (VLD1), translate-and-validate with two VTBL nibble LUTs
// (lutLo by low nibble, lutHi by high nibble): a char is valid iff (lo & hi) == 0;
// the 16-byte AND is reduced to a GPR (two VMOV D-lane extracts + ORR) and on ANY
// invalid byte (whitespace, padding '=', non-alphabet) the kernel returns the
// number of groups decoded so far so the caller re-decodes the remainder with
// encoding/base64 (errors + padded tail stay byte/offset-identical). The 6-bit
// value is char + VTBL(lutRoll, hiNibble + (char==0x2f ? 0xFF : 0)).
//
// Pack 4x6-bit -> 3 bytes, per LITTLE-ENDIAN 32-bit lane W = a|b<<8|c<<16|d<<24:
//
//	P = (W<<2 & 0x0000FC) | (W>>12 & 0x000003)   // o0
//	  | (W<<4 & 0x00F000) | (W>>10 & 0x000F00)   // o1
//	  | (W<<6 & 0xC00000) | (W>>8  & 0x3F0000)   // o2
//
// so each lane holds bytes [o0,o1,o2,0]; a final VTBL compacts the 12 meaningful
// bytes (lane indices 0,1,2 / 4,5,6 / 8,9,10 / 12,13,14) into the low 12 output
// bytes and VST1 stores them. Run: go run decode_arm64_gen.go
package main

import (
	"fmt"
	"os"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/arm64"
	"github.com/go-asmgen/asmgen/emit"
)

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

	lutLo := f.Data("darmLutLo", []byte{0x15, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x13, 0x1A, 0x1B, 0x1B, 0x1B, 0x1A})
	lutHi := f.Data("darmLutHi", []byte{0x10, 0x10, 0x01, 0x02, 0x04, 0x08, 0x04, 0x08, 0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x10})
	lutRoll := f.Data("darmLutRoll", []byte{0, 16, 19, 4, 0xBF, 0xBF, 0xB9, 0xB9, 0, 0, 0, 0, 0, 0, 0, 0})
	// Pack field masks (one value per 32-bit lane).
	k0a := f.Data("darmK0a", rep4(0x000000FC)) // W<<2  -> o0 high
	k0b := f.Data("darmK0b", rep4(0x00000003)) // W>>12 -> o0 low
	k1a := f.Data("darmK1a", rep4(0x0000F000)) // W<<4  -> o1 high
	k1b := f.Data("darmK1b", rep4(0x00000F00)) // W>>10 -> o1 low
	k2a := f.Data("darmK2a", rep4(0x00C00000)) // W<<6  -> o2 high
	k2b := f.Data("darmK2b", rep4(0x003F0000)) // W>>8  -> o2 low
	// Final compaction VTBL control: gather lane bytes [0,1,2,4,5,6,8,9,10,12,13,14]
	// into bytes 0..11; the high 4 read out-of-range -> VTBL yields 0.
	pshuf := f.Data("darmPshuf", []byte{0, 1, 2, 4, 5, 6, 8, 9, 10, 12, 13, 14, 0xFF, 0xFF, 0xFF, 0xFF})

	sig := abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		[]abi.Arg{abi.Scalar("ret", abi.Int64)},
	)

	b := arm64.NewFunc("decodeBlocks", sig, 0)
	b.LoadArg("dst_base", "R0").
		LoadArg("src_base", "R1").
		LoadArg("n", "R2").
		// Load constant tables once.
		Raw("MOVD $%s(SB), R4", lutLo).Raw("VLD1 (R4), [V8.B16]").
		Raw("MOVD $%s(SB), R4", lutHi).Raw("VLD1 (R4), [V9.B16]").
		Raw("MOVD $%s(SB), R4", lutRoll).Raw("VLD1 (R4), [V10.B16]").
		Raw("MOVD $%s(SB), R4", k0a).Raw("VLD1 (R4), [V11.B16]").
		Raw("MOVD $%s(SB), R4", k0b).Raw("VLD1 (R4), [V12.B16]").
		Raw("MOVD $%s(SB), R4", k1a).Raw("VLD1 (R4), [V13.B16]").
		Raw("MOVD $%s(SB), R4", k1b).Raw("VLD1 (R4), [V14.B16]").
		Raw("MOVD $%s(SB), R4", k2a).Raw("VLD1 (R4), [V15.B16]").
		Raw("MOVD $%s(SB), R4", k2b).Raw("VLD1 (R4), [V16.B16]").
		Raw("MOVD $%s(SB), R4", pshuf).Raw("VLD1 (R4), [V17.B16]").
		Raw("VMOVI $15, V18.B16"). // 0x0f mask
		Raw("VMOVI $47, V19.B16"). // 0x2f ('/')
		Raw("MOVD $0, R3").        // R3 = group counter
		Raw("CBZ R2, done").
		Label("loop").
		Raw("VLD1 (R1), [V0.B16]"). // V0 = 16 ASCII chars
		// Nibbles: V1 = lo = char & 0x0f ; V2 = hi = char >> 4.
		Raw("VAND V18.B16, V0.B16, V1.B16").
		Raw("VUSHR $4, V0.B16, V2.B16").
		// Translate LUTs.
		Raw("VTBL V1.B16, [V8.B16], V3.B16"). // lo = lutLo[loNibble]
		Raw("VTBL V2.B16, [V9.B16], V4.B16"). // hi = lutHi[hiNibble]
		// Validity: err = lo & hi ; reduce to a GPR and bail if nonzero.
		Raw("VAND V3.B16, V4.B16, V5.B16").
		Raw("VMOV V5.D[0], R5").
		Raw("VMOV V5.D[1], R6").
		Raw("ORR R6, R5, R5").
		Raw("CBNZ R5, done").
		// roll index = hi + (char==0x2f ? 0xFF : 0).
		Raw("VCMEQ V19.B16, V0.B16, V6.B16").  // 0xFF where char == '/'
		Raw("VADD V2.B16, V6.B16, V6.B16").    // hi + eq2f
		Raw("VTBL V6.B16, [V10.B16], V6.B16"). // roll = lutRoll[index]
		Raw("VADD V0.B16, V6.B16, V0.B16").    // V0 = 6-bit values (a,b,c,d,...)
		// Pack: per LE 32-bit lane W -> P = [o0,o1,o2,0].
		Raw("VSHL $2, V0.S4, V1.S4").Raw("VAND V11.B16, V1.B16, V1.B16").   // (W<<2)&0xFC
		Raw("VUSHR $12, V0.S4, V2.S4").Raw("VAND V12.B16, V2.B16, V2.B16"). // (W>>12)&0x03
		Raw("VORR V2.B16, V1.B16, V1.B16").
		Raw("VSHL $4, V0.S4, V2.S4").Raw("VAND V13.B16, V2.B16, V2.B16"). // (W<<4)&0xF000
		Raw("VORR V2.B16, V1.B16, V1.B16").
		Raw("VUSHR $10, V0.S4, V2.S4").Raw("VAND V14.B16, V2.B16, V2.B16"). // (W>>10)&0xF00
		Raw("VORR V2.B16, V1.B16, V1.B16").
		Raw("VSHL $6, V0.S4, V2.S4").Raw("VAND V15.B16, V2.B16, V2.B16"). // (W<<6)&0xC00000
		Raw("VORR V2.B16, V1.B16, V1.B16").
		Raw("VUSHR $8, V0.S4, V2.S4").Raw("VAND V16.B16, V2.B16, V2.B16"). // (W>>8)&0x3F0000
		Raw("VORR V2.B16, V1.B16, V1.B16").                                // V1 = packed lanes [o0,o1,o2,0]
		// Compact the 12 meaningful bytes into the low 12 lanes.
		Raw("VTBL V17.B16, [V1.B16], V1.B16").
		Raw("VST1 [V1.B16], (R0)"). // store 16 (low 12 meaningful)
		Raw("ADD $16, R1").
		Raw("ADD $12, R0").
		Raw("ADD $1, R3").
		Raw("SUB $1, R2").
		Raw("CBNZ R2, loop").
		Label("done").
		Raw("MOVD R3, ret+56(FP)").
		Ret()
	f.Add(b.Func())

	if err := os.WriteFile("decode_arm64.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote decode_arm64.s")
}
