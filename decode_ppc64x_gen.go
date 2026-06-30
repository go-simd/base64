//go:build ignore

// Command gen produces decode_ppc64x.s with go-asmgen: Lemire/Muła vectorised
// base64 *decode* for ppc64le using the VSX/AltiVec vector facility.
//
// Per 16 ASCII chars (loaded with LXVB16X, byte-order-correct on little-endian),
// translate-and-validate with two VPERM nibble LUTs (lutLo by low nibble, lutHi by
// high nibble): a char is valid iff (lo & hi) == 0 across the whole vector, tested
// with the record-form VCMPEQUB. against zero (sets CR6) — on ANY invalid byte the
// kernel returns the number of groups decoded so far and the caller re-decodes the
// remainder with encoding/base64 (errors + padded tail stay byte/offset-identical).
// The 6-bit value is char + VPERM(lutRoll, hiNibble + (char==0x2f ? 0xFF : 0)).
//
// Pack 4x6-bit -> 3 bytes with right-shift-only extraction: each 32-bit BIG-ENDIAN
// word after translation is W = a<<24|b<<16|c<<8|d (a..d are 6-bit); the 24-bit
// value lands at bits 23..0 as P = (W>>6 & 0xFC0000)|(W>>4 & 0x3F000)|(W>>2 & 0xFC0)
// |(W & 0x3F), so each word holds bytes [0,o0,o1,o2]. A final VPERM compacts the
// three meaningful bytes of the four words into the low 12 output bytes; STXVB16X
// stores them.
//
// VSX<->VMX aliasing: vectors are loaded with LXVB16X into VS(32+k) and operated on
// as Vk (the AltiVec name aliasing VS(32+k)); the store uses STXVB16X VS(32+k).
//
// Run: GOWORK=off go run decode_ppc64x_gen.go
package main

import (
	"fmt"
	"os"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/emit"
	"github.com/go-asmgen/asmgen/ppc64"
)

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

func splat16(x byte) []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = x
	}
	return b
}

// decode variant: translate/validate nibble LUTs + roll LUT, the special roll char
// and the roll-index delta mask K. Std (+/) special='/'(0x2f) K=0xFF (delta -1);
// URL (-_) special='_'(0x5f) K=0xFB (delta -5) — '_' shares a non-empty neighbour
// nibble so the simple -1 collides. The URL LUTs are derived by an exhaustive
// biclique cover and self-verified against every byte (see decode_gen.go).
type decVariant struct {
	suffix                string
	lutLo, lutHi, lutRoll []byte
	special, k            byte
}

var decVariants = []decVariant{
	{"", []byte{0x15, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x13, 0x1A, 0x1B, 0x1B, 0x1B, 0x1A},
		[]byte{0x10, 0x10, 0x01, 0x02, 0x04, 0x08, 0x04, 0x08, 0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x10},
		[]byte{0, 16, 19, 4, 0xBF, 0xBF, 0xB9, 0xB9, 0, 0, 0, 0, 0, 0, 0, 0}, 0x2f, 0xFF},
	{"URL", []byte{0x23, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x03, 0x07, 0x1F, 0x1F, 0x1D, 0x1F, 0x0F},
		[]byte{0x01, 0x01, 0x02, 0x04, 0x20, 0x10, 0x20, 0x08, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01},
		[]byte{0xE0, 0, 0x11, 4, 0xBF, 0xBF, 0xB9, 0xB9, 0, 0, 0, 0, 0, 0, 0, 0}, 0x5f, 0xFB},
}

func main() {
	f := emit.NewFile("ppc64le")

	// Per-word right-shift counts (big-endian-per-lane, like the encode kernel).
	s6 := f.Data("dppcS6", rep4be(6))
	s4 := f.Data("dppcS4", rep4be(4))
	s2 := f.Data("dppcS2", rep4be(2))
	// Per-word field masks after each shift.
	m0 := f.Data("dppcM0", rep4be(0x00FC0000)) // byte o0, after W>>6
	m1 := f.Data("dppcM1", rep4be(0x0003F000)) // byte o1, after W>>4
	m2 := f.Data("dppcM2", rep4be(0x00000FC0)) // byte o2 high, after W>>2
	m3 := f.Data("dppcM3", rep4be(0x0000003F)) // byte o2 low, no shift
	// Final compaction VPERM control: gather the 3 valid bytes of each of the four
	// words (vector indices 1,2,3 / 5,6,7 / 9,10,11 / 13,14,15) into bytes 0..11.
	pshuf := f.Data("dppcPshuf", []byte{1, 2, 3, 5, 6, 7, 9, 10, 11, 13, 14, 15, 0, 0, 0, 0})
	zero := f.Data("dppcZero", make([]byte, 16))
	c0f := f.Data("dppcC0f", splat16(0x0f))

	sig := abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		[]abi.Arg{abi.Scalar("ret", abi.Int64)},
	)

	for _, vr := range decVariants {
		lutLo := f.Data("dppcLutLo"+vr.suffix, vr.lutLo)
		lutHi := f.Data("dppcLutHi"+vr.suffix, vr.lutHi)
		lutRoll := f.Data("dppcLutRoll"+vr.suffix, vr.lutRoll)
		ceq := f.Data("dppcCeq"+vr.suffix, splat16(vr.special)) // special-char splat
		urlSafe := vr.suffix == "URL"
		var ck string
		if urlSafe {
			ck = f.Data("dppcCk"+vr.suffix, splat16(vr.k)) // roll-index mask K
		}

		b := ppc64.NewFunc("decodeBlocks"+vr.suffix, sig, 0)
		b.LoadArg("dst_base", "R3").
			LoadArg("src_base", "R4").
			LoadArg("n", "R5").
			// Load constant tables (LXVB16X -> VS(32+k), used as Vk).
			Raw("MOVD $%s(SB), R6", lutLo).Raw("LXVB16X (R6)(R0), VS39").   // V7  = lutLo
			Raw("MOVD $%s(SB), R6", lutHi).Raw("LXVB16X (R6)(R0), VS40").   // V8  = lutHi
			Raw("MOVD $%s(SB), R6", lutRoll).Raw("LXVB16X (R6)(R0), VS41"). // V9  = lutRoll
			Raw("MOVD $%s(SB), R6", s6).Raw("LXVB16X (R6)(R0), VS42").      // V10 = shift 6
			Raw("MOVD $%s(SB), R6", s4).Raw("LXVB16X (R6)(R0), VS43").      // V11 = shift 4
			Raw("MOVD $%s(SB), R6", s2).Raw("LXVB16X (R6)(R0), VS44").      // V12 = shift 2
			Raw("MOVD $%s(SB), R6", m0).Raw("LXVB16X (R6)(R0), VS45").      // V13 = mask o0
			Raw("MOVD $%s(SB), R6", m1).Raw("LXVB16X (R6)(R0), VS46").      // V14 = mask o1
			Raw("MOVD $%s(SB), R6", m2).Raw("LXVB16X (R6)(R0), VS47").      // V15 = mask o2hi
			Raw("MOVD $%s(SB), R6", m3).Raw("LXVB16X (R6)(R0), VS48").      // V16 = mask o2lo
			Raw("MOVD $%s(SB), R6", pshuf).Raw("LXVB16X (R6)(R0), VS49").   // V17 = compaction ctrl
			Raw("MOVD $%s(SB), R6", ceq).Raw("LXVB16X (R6)(R0), VS50").     // V18 = special-char splat
			Raw("MOVD $%s(SB), R6", zero).Raw("LXVB16X (R6)(R0), VS51").    // V19 = 0
			Raw("MOVD $%s(SB), R6", c0f).Raw("LXVB16X (R6)(R0), VS52").     // V20 = 0x0f
			Raw("VSPLTISB $4, V21").                                        // V21 = 4 (VSRB count)
			Raw("MOVD $0, R7").                                             // R7 = group counter
			Raw("CMP R5, $0").Raw("BEQ done").
			Label("loop").
			Raw("LXVB16X (R4)(R0), VS32"). // V0 = 16 ASCII chars
			// Nibbles: V1 = lo = char & 0x0f ; V2 = hi = char >> 4 (byte shift).
			Raw("VAND V0, V20, V1").
			Raw("VSRB V0, V21, V2"). // V21 = splat-4
			// Translate LUTs: V3 = VPERM(lutLo, lutLo, lo) ; V4 = VPERM(lutHi, lutHi, hi).
			Raw("VPERM V7, V7, V1, V3").
			Raw("VPERM V8, V8, V2, V4").
			// Validity: err = lo & hi ; if err != 0 anywhere, bail. VCMPEQUBCC against 0
			// sets CR6[eq]=1 iff every byte is zero (valid); branch out when not all-zero.
			Raw("VAND V3, V4, V5").
			Raw("VCMPEQUBCC V5, V19, V6").
			Raw("BC 4, 24, done"). // branch if CR6[eq] == 0 (not all equal -> some err byte)
			// roll index = hi + ((char==special) ? delta : 0). The eq-mask is 0x00/0xFF;
			// Std's delta is the mask (-1); URL ANDs it with K to get -5.
			Raw("VCMPEQUB V0, V18, V6") // 0xFF where char==special
		if urlSafe {
			// V22 = K splat; delta = eqMask & K (0x00 or 0xFF&K).
			b.Raw("MOVD $%s(SB), R6", ck).Raw("LXVB16X (R6)(R0), VS54").
				Raw("VAND V6, V22, V6")
		}
		b.Raw("VADDUBM V2, V6, V6"). // hi + delta
						Raw("VPERM V9, V9, V6, V6"). // roll = lutRoll[index]
						Raw("VADDUBM V0, V6, V0").   // V0 = 6-bit values (a,b,c,d,...)
			// Pack: per BE word W = a<<24|b<<16|c<<8|d ->
			// P = (W>>6 & m0)|(W>>4 & m1)|(W>>2 & m2)|(W & m3).
			Raw("VSRW V0, V10, V1").Raw("VAND V1, V13, V1").
			Raw("VSRW V0, V11, V2").Raw("VAND V2, V14, V2").Raw("VOR V2, V1, V1").
			Raw("VSRW V0, V12, V2").Raw("VAND V2, V15, V2").Raw("VOR V2, V1, V1").
			Raw("VAND V0, V16, V2").Raw("VOR V2, V1, V1"). // V1 = packed words [0,o0,o1,o2]
			// Compact the 12 meaningful bytes into the low 12 lanes.
			Raw("VPERM V1, V1, V17, V1").
			Raw("STXVB16X VS33, (R3)(R0)"). // store V1 (writes 16, low 12 meaningful)
			Raw("ADD $16, R4").
			Raw("ADD $12, R3").
			Raw("ADD $1, R7").
			Raw("ADD $-1, R5").
			Raw("CMP R5, $0").Raw("BNE loop").
			Label("done").
			Raw("MOVD R7, ret+56(FP)").
			Ret()
		f.Add(b.Func())
	}

	if err := os.WriteFile("decode_ppc64x.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote decode_ppc64x.s")
}
