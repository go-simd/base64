//go:build ignore

// Command gen produces encode_ppc64x.s with go-asmgen: Lemire/Muła vectorised
// base64 encode for ppc64le using the VSX/AltiVec vector facility. Per 12 input
// bytes (loaded as 16 via LXVB16X, which is byte-order-correct on little-endian —
// LXVD2X would swap doublewords), a VPERM spread places one 24-bit group in each
// 32-bit lane as [b1,b0,b2,b1]; the four 6-bit indices are pulled out with
// per-word shifts (VSLW/VSRW driven by splatted shift-count vectors) and VAND
// masks, then a VPERM offset-LUT maps each index to its ASCII byte. The SSE range
// bucket is rebuilt from VSUBUBS (saturating unsigned byte subtract) and VCMPGTUB
// (unsigned byte compare-greater). Constant tables come from emit.File.Data.
//
// VSX↔VMX aliasing: vectors are loaded with LXVB16X into VS(32+k) and operated on
// as Vk (the AltiVec name aliasing VS(32+k)); the store uses STXVB16X VS(32+k).
//
// Run: GOWORK=off go run encode_ppc64x_gen.go
package main

import (
	"fmt"
	"os"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/emit"
	"github.com/go-asmgen/asmgen/ppc64"
)

// rep4be repeats a 32-bit constant v into all four 32-bit lanes, stored so the
// in-register big-endian word value (as VSRW/VAND see it after an LXVB16X load)
// equals v: lowest address byte = most-significant byte.
func rep4be(v uint32) []byte {
	b := make([]byte, 16)
	for i := 0; i < 4; i++ {
		b[i*4+0] = byte(v >> 24)
		b[i*4+1] = byte(v >> 16)
		b[i*4+2] = byte(v >> 8)
		b[i*4+3] = byte(v)
	}
	return b
}

func repByte(x byte) []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = x
	}
	return b
}

func main() {
	f := emit.NewFile("ppc64le")

	// Spread control for VPERM: per 32-bit output lane i, place input bytes
	// [b0,b1,b2,0] (lowest address first), i.e. result bytes {3i, 3i+1, 3i+2, X}.
	// The 4th byte is masked out later, so any in-range index (0) is fine. With an
	// LXVB16X (LE byte-order-preserving) load the AltiVec VPERM index is PSHUFB-style
	// (index i in [0,15] selects byte i of Va), so the control vector holds those
	// source indices directly. (Verified position-dependent under qemu.)
	spread := []byte{0, 1, 2, 0, 3, 4, 5, 0, 6, 7, 8, 0, 9, 10, 11, 0}
	shuf := f.Data("ppcShuf", spread)
	// After the spread, each lane's in-register BIG-ENDIAN word is W = b0<<24 |
	// b1<<16 | b2<<8. The four 6-bit base64 indices land in their output byte
	// positions with right-shift-only extraction (VSRW interprets the word
	// big-endian): out byte0 = (W>>2)&0x3f000000, byte1 = (W>>4)&0x003f0000,
	// byte2 = (W>>6)&0x00003f00, byte3 = (W>>8)&0x0000003f. Masks are stored
	// big-endian-per-lane to match.
	m0 := f.Data("ppcM0", rep4be(0x3f000000)) // byte0 mask (after >>2)
	m1 := f.Data("ppcM1", rep4be(0x003f0000)) // byte1 mask (after >>4)
	m2 := f.Data("ppcM2", rep4be(0x00003f00)) // byte2 mask (after >>6)
	m3 := f.Data("ppcM3", rep4be(0x0000003f)) // byte3 mask (after >>8)
	// Splatted shift counts, one per 32-bit word (big-endian word shift).
	s2 := f.Data("ppcS2", rep4be(2))
	s4 := f.Data("ppcS4", rep4be(4))
	s6 := f.Data("ppcS6", rep4be(6))
	s8 := f.Data("ppcS8", rep4be(8))
	// Range-bucket constants (byte-splatted).
	c51 := f.Data("ppcC51", repByte(51))
	c25 := f.Data("ppcC25", repByte(25))
	c1 := f.Data("ppcC1", repByte(1))
	// ASCII offset LUT, indexed by range bucket (0..12). With LXVB16X + the PSHUFB-
	// style VPERM (index i in [0,15] selects Va byte i), the bucket value is used as
	// the VPERM control directly — no big-endian index fix-up needed.
	lut := f.Data("ppcLut", []byte{65, 71, 252, 252, 252, 252, 252, 252, 252, 252, 252, 252, 237, 240, 0, 0})

	sig := abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		nil,
	)

	b := ppc64.NewFunc("encodeBlocks", sig, 0)
	b.LoadArg("dst_base", "R3").
		LoadArg("src_base", "R4").
		LoadArg("n", "R5").
		// Load constant tables. Each LXVB16X targets VS(32+k); the value is then
		// used under its AltiVec name Vk (which aliases VS(32+k)).
		Raw("MOVD $%s(SB), R6", shuf).Raw("LXVB16X (R6)(R0), VS39"). // V7 = spread ctrl
		Raw("MOVD $%s(SB), R6", m0).Raw("LXVB16X (R6)(R0), VS40").   // V8  = byte0 mask
		Raw("MOVD $%s(SB), R6", m1).Raw("LXVB16X (R6)(R0), VS41").   // V9  = byte1 mask
		Raw("MOVD $%s(SB), R6", m2).Raw("LXVB16X (R6)(R0), VS42").   // V10 = byte2 mask
		Raw("MOVD $%s(SB), R6", m3).Raw("LXVB16X (R6)(R0), VS43").   // V11 = byte3 mask
		Raw("MOVD $%s(SB), R6", lut).Raw("LXVB16X (R6)(R0), VS44").  // V12 = ASCII LUT
		Raw("MOVD $%s(SB), R6", s2).Raw("LXVB16X (R6)(R0), VS45").   // V13 = shift 2
		Raw("MOVD $%s(SB), R6", s4).Raw("LXVB16X (R6)(R0), VS46").   // V14 = shift 4
		Raw("MOVD $%s(SB), R6", s6).Raw("LXVB16X (R6)(R0), VS47").   // V15 = shift 6
		Raw("MOVD $%s(SB), R6", s8).Raw("LXVB16X (R6)(R0), VS48").   // V16 = shift 8
		Raw("MOVD $%s(SB), R6", c51).Raw("LXVB16X (R6)(R0), VS49").  // V17 = 51
		Raw("MOVD $%s(SB), R6", c25).Raw("LXVB16X (R6)(R0), VS50").  // V18 = 25
		Raw("MOVD $%s(SB), R6", c1).Raw("LXVB16X (R6)(R0), VS51").   // V19 = 1
		Raw("CMP R5, $0").Raw("BEQ done").
		Label("loop").
		// Load 16 bytes (use first 12), spread each lane to bytes [b0,b1,b2,0].
		Raw("LXVB16X (R4)(R0), VS32"). // V0 = src bytes
		Raw("VPERM V0, V0, V7, V0").   // spread -> per-lane big-endian word W = b0<<24|b1<<16|b2<<8
		// Right-shift-only index extraction. Go ppc64 asm shift form is
		// "VSRW Vdata, Vshift, Vdst" => Vdst = Vdata >> Vshift (per 32-bit big-endian
		// word; the shift count is the low bits of each shift-vector word, which is
		// why the count tables are stored big-endian-per-lane via rep4be).
		Raw("VSRW V0, V13, V1").Raw("VAND V1, V8, V1").  // byte0 = (W>>2)&0x3f000000
		Raw("VSRW V0, V14, V2").Raw("VAND V2, V9, V2").  // byte1 = (W>>4)&0x003f0000
		Raw("VOR V2, V1, V1").
		Raw("VSRW V0, V15, V2").Raw("VAND V2, V10, V2"). // byte2 = (W>>6)&0x00003f00
		Raw("VOR V2, V1, V1").
		Raw("VSRW V0, V16, V2").Raw("VAND V2, V11, V2"). // byte3 = (W>>8)&0x0000003f
		Raw("VOR V2, V1, V1"). // V1 = packed 6-bit indices, one per byte
		// Range bucket: sat = idx -sat 51 ; gt = idx>25 ; bucket = sat + (gt&1).
		// VSUBUBS Va,Vb,Vt => Vt = sat(Va-Vb); VCMPGTUB Va,Vb,Vt => Vt = (Va>Vb).
		Raw("VSUBUBS V1, V17, V2").  // sat = idx - 51 (saturating)
		Raw("VCMPGTUB V1, V18, V3"). // 0xff where idx>25
		Raw("VAND V3, V19, V3").     // &1
		Raw("VADDUBM V2, V3, V2").   // bucket
		// VPERM LUT lookup: bucket selects lut[bucket] directly (PSHUFB-style index).
		Raw("VPERM V12, V12, V2, V2").
		Raw("VADDUBM V1, V2, V2"). // ascii = idx + offset
		Raw("STXVB16X VS34, (R3)(R0)").
		Raw("ADD $12, R4").
		Raw("ADD $16, R3").
		Raw("ADD $-1, R5").
		Raw("CMP R5, $0").Raw("BNE loop").
		Label("done").
		Ret()
	f.Add(b.Func())

	if err := os.WriteFile("encode_ppc64x.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote encode_ppc64x.s")
}
