//go:build ignore

// Command gen produces decode_s390x.s with go-asmgen: Lemire/Muła vectorised
// base64 *decode* for s390x using the z/Architecture vector facility. s390x is
// big-endian, so VL loads 16 chars with lane 0 = lowest address and the fullword
// vector arithmetic operates on naturally big-endian words — exactly what the
// right-shift pack wants, so no little-endian fix-up is needed.
//
// Per 16 ASCII chars, translate-and-validate with two VPERM nibble LUTs (lutLo by
// low nibble, lutHi by high nibble): a char is valid iff (lo & hi) == 0 across the
// whole vector, tested with VCEQBS against zero (sets the condition code; CC==0
// means every byte matched -> valid). On ANY invalid byte the kernel returns the
// number of groups decoded so far and the caller re-decodes the remainder with
// encoding/base64 (errors + padded tail stay byte/offset-identical). The 6-bit
// value is char + VPERM(lutRoll, hiNibble + (char==0x2f ? 0xFF : 0)).
//
// Pack 4x6-bit -> 3 bytes with right-shift-only extraction on each big-endian
// fullword W = a<<24|b<<16|c<<8|d (a..d are 6-bit): P = (W>>6 & 0xFC0000) |
// (W>>4 & 0x3F000) | (W>>2 & 0xFC0) | (W & 0x3F), so each word holds bytes
// [0,o0,o1,o2]; a final VPERM compacts the three meaningful bytes of the four
// words into the low 12 output bytes and VST stores them.
//
// The cross-lane VPERM shuffles (the two LUT lookups, the roll lookup, and the
// compaction) are the only places big-endian lane numbering could bite; the whole
// kernel is verified position-dependently under qemu, with the FuzzDecode gate
// confirming byte/error-identical output to encoding/base64.
//
// Run: GOWORK=off go run decode_s390x_gen.go
package main

import (
	"fmt"
	"os"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/emit"
	"github.com/go-asmgen/asmgen/s390x"
)

func be4(v uint32) []byte {
	b := make([]byte, 16)
	for i := 0; i < 4; i++ {
		b[i*4+0] = byte(v >> 24)
		b[i*4+1] = byte(v >> 16)
		b[i*4+2] = byte(v >> 8)
		b[i*4+3] = byte(v)
	}
	return b
}

func splat(x byte) []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = x
	}
	return b
}

// decode variant: translate/validate nibble LUTs + roll LUT, the special roll char
// and the roll-index delta mask K. Std (+/) special='/'(0x2f) K=0xFF (delta -1);
// URL (-_) special='_'(0x5f) K=0xFB (delta -5). The URL LUTs are derived by an
// exhaustive biclique cover and self-verified against every byte (see decode_gen.go).
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
	f := emit.NewFile("s390x")

	m0 := f.Data("ds390M0", be4(0x00FC0000)) // o0, after W>>6
	m1 := f.Data("ds390M1", be4(0x0003F000)) // o1, after W>>4
	m2 := f.Data("ds390M2", be4(0x00000FC0)) // o2 high, after W>>2
	m3 := f.Data("ds390M3", be4(0x0000003F)) // o2 low, no shift
	c0f := f.Data("ds390C0f", splat(0x0f))
	// Compaction VPERM control (big-endian lane order, lane 0 = lowest address):
	// gather the 3 valid bytes of each of the four words (lanes 1,2,3 / 5,6,7 /
	// 9,10,11 / 13,14,15) into output lanes 0..11.
	pshuf := f.Data("ds390Pshuf", []byte{1, 2, 3, 5, 6, 7, 9, 10, 11, 13, 14, 15, 0, 0, 0, 0})

	sig := abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		[]abi.Arg{abi.Scalar("ret", abi.Int64)},
	)

	for _, vr := range decVariants {
		lutLo := f.Data("ds390LutLo"+vr.suffix, vr.lutLo)
		lutHi := f.Data("ds390LutHi"+vr.suffix, vr.lutHi)
		lutRoll := f.Data("ds390LutRoll"+vr.suffix, vr.lutRoll)
		ceq := f.Data("ds390Ceq"+vr.suffix, splat(vr.special)) // special-char splat
		urlSafe := vr.suffix == "URL"
		var ck string
		if urlSafe {
			ck = f.Data("ds390Ck"+vr.suffix, splat(vr.k)) // roll-index mask K
		}

		g := s390x.NewFunc("decodeBlocks"+vr.suffix, sig, 0)
		g.LoadArg("dst_base", "R1").
			LoadArg("src_base", "R2").
			LoadArg("n", "R3").
			Raw("MOVD $%s(SB), R4", lutLo).Raw("VL (R4), V16").
			Raw("MOVD $%s(SB), R4", lutHi).Raw("VL (R4), V17").
			Raw("MOVD $%s(SB), R4", lutRoll).Raw("VL (R4), V18").
			Raw("MOVD $%s(SB), R4", m0).Raw("VL (R4), V19").
			Raw("MOVD $%s(SB), R4", m1).Raw("VL (R4), V20").
			Raw("MOVD $%s(SB), R4", m2).Raw("VL (R4), V21").
			Raw("MOVD $%s(SB), R4", m3).Raw("VL (R4), V22").
			Raw("MOVD $%s(SB), R4", c0f).Raw("VL (R4), V23").
			Raw("MOVD $%s(SB), R4", ceq).Raw("VL (R4), V24").
			Raw("MOVD $%s(SB), R4", pshuf).Raw("VL (R4), V25").
			Raw("VZERO V26").   // zero vector for the validity compare
			Raw("MOVD $0, R5"). // R5 = group counter
			Raw("CMPBEQ R3, $0, done").
			Label("loop").
			Raw("VL (R2), V0"). // V0 = 16 ASCII chars
			// Nibbles: V1 = lo = char & 0x0f ; V2 = hi = char >> 4 (byte shift).
			Raw("VN V0, V23, V1").
			Raw("VESRLB $4, V0, V2").
			// Translate LUTs (VPERM Va,Vb,Vctrl,Vdst selects from concatenated [Va:Vb]).
			Raw("VPERM V16, V16, V1, V3"). // lo = lutLo[loNibble]
			Raw("VPERM V17, V17, V2, V4"). // hi = lutHi[hiNibble]
			// Validity: err = lo & hi ; VCEQBS sets CC==0 iff every byte equals zero
			// (valid). Branch out otherwise (BNE = CC != 0 = some byte differs).
			Raw("VN V3, V4, V5").
			Raw("VCEQBS V5, V26, V6").
			Raw("BNE done").
			// roll index = hi + ((char==special) ? delta : 0). eq-mask is 0x00/0xFF;
			// Std's delta is the mask (-1); URL ANDs it with K to get -5.
			Raw("VCEQB V0, V24, V6") // 0xFF where char==special
		if urlSafe {
			// V27 = K splat; delta = eqMask & K.
			g.Raw("MOVD $%s(SB), R4", ck).Raw("VL (R4), V27").
				Raw("VN V6, V27, V6")
		}
		g.Raw("VAB V2, V6, V6"). // hi + delta
						Raw("VPERM V18, V18, V6, V6"). // roll = lutRoll[index]
						Raw("VAB V0, V6, V0").         // V0 = 6-bit values (a,b,c,d,...)
			// Pack: per BE word W = a<<24|b<<16|c<<8|d ->
			// P = (W>>6 & m0)|(W>>4 & m1)|(W>>2 & m2)|(W & m3).
			Raw("VESRLF $6, V0, V1").Raw("VN V1, V19, V1").
			Raw("VESRLF $4, V0, V2").Raw("VN V2, V20, V2").Raw("VO V2, V1, V1").
			Raw("VESRLF $2, V0, V2").Raw("VN V2, V21, V2").Raw("VO V2, V1, V1").
			Raw("VN V0, V22, V2").Raw("VO V2, V1, V1"). // V1 = packed words [0,o0,o1,o2]
			// Compact the 12 meaningful bytes into the low 12 lanes.
			Raw("VPERM V1, V1, V25, V1").
			Raw("VST V1, (R1)"). // store 16 (low 12 meaningful)
			Raw("ADD $16, R2").
			Raw("ADD $12, R1").
			Raw("ADD $1, R5").
			Raw("ADD $-1, R3").
			Raw("CMPBNE R3, $0, loop").
			Label("done").
			Raw("MOVD R5, ret+56(FP)").
			Ret()
		f.Add(g.Func())
	}

	if err := os.WriteFile("decode_s390x.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote decode_s390x.s")
}
