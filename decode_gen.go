//go:build ignore

// Command gen produces decode_amd64.s with go-asmgen: Lemire/Muła's vectorised
// base64 *decode*, both an SSSE3 path (16 input chars -> 12 bytes per 128-bit
// block) and an AVX2 path (32 -> 24 per 256-bit block).
//
// Per block: load the ASCII chars, then translate-and-validate with the classic
// two-PSHUFB nibble LUTs (lutLo keyed by the low nibble, lutHi keyed by the high
// nibble). A byte is a valid base64 char iff (lo & hi) == 0; this is PTEST/VPTEST-
// tested over the whole vector (the error bits are not guaranteed to land in bit 7,
// so PMOVMSKB would miss them) and, on ANY invalid byte (whitespace, padding '=',
// or any non-alphabet char), the kernel returns the number of groups decoded so
// far so the caller re-decodes the remainder with encoding/base64 (errors + padded
// tail stay byte/offset-identical to the stdlib). The 6-bit value is produced by
// adding a PSHUFB "roll" offset keyed by (hi_nibble + (char=='/' ? -1 : 0)).
//
// Pack 4x6-bit -> 3 bytes: PMADDUBSW with [0x01,0x40,...] merges (a*64+b) and
// (c*64+d) into 16-bit words, PMADDWD with [0x10,0x10,0x00,0x01,...] merges those
// into a 24-bit value packed (little-endian) as [o2,o1,o0,0] per dword; a final
// PSHUFB compacts the three meaningful bytes of each dword into the low 12 bytes
// and they are stored. Run: go run decode_gen.go
package main

import (
	"fmt"
	"os"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/amd64"
	"github.com/go-asmgen/asmgen/emit"
)

func rep(v []byte, times int) []byte {
	var b []byte
	for i := 0; i < times; i++ {
		b = append(b, v...)
	}
	return b
}

var (
	// Translate/validate nibble LUTs (Muła). Stored as raw bytes (two's-complement
	// for the negative roll entries).
	lutLoBytes   = []byte{0x15, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x13, 0x1A, 0x1B, 0x1B, 0x1B, 0x1A}
	lutHiBytes   = []byte{0x10, 0x10, 0x01, 0x02, 0x04, 0x08, 0x04, 0x08, 0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x10}
	lutRollBytes = []byte{0, 16, 19, 4, 0xBF, 0xBF, 0xB9, 0xB9, 0, 0, 0, 0, 0, 0, 0, 0} // -65=0xBF, -71=0xB9
	// maddubs multiplier: per byte pair (a,b) -> a*0x40 + b*1, so the first 6-bit
	// value lands in the high bits. PMADDUBSW multiplies the (unsigned) data byte by
	// the (signed) multiplier byte position-for-position, so the pattern is
	// [0x40,0x01] repeated.
	maddubsBytes = []byte{0x40, 0x01}
	// madd (PMADDWD) word multipliers: dword = w0*0x0010 + w1*0x0001? We want
	// (a*64+b)*0x1000 + (c*64+d). PMADDWD multiplies signed 16-bit pairs; pattern
	// per dword is [w0_mul, w1_mul] = [0x1000, 0x0001].
	maddwdWords = []byte{0x00, 0x10, 0x01, 0x00} // little-endian: 0x1000, 0x0001
	// Final compaction: gather [o0,o1,o2] from each dword (LE bytes [2,1,0]).
	packShuf = []byte{2, 1, 0, 6, 5, 4, 10, 9, 8, 14, 13, 12, 0x80, 0x80, 0x80, 0x80}
	// AVX2 cross-lane dword permute: after the per-lane compaction, lane0's 12
	// valid bytes sit in dwords 0..2 and lane1's in dwords 4..6; pull them together
	// into output dwords 0..5 (24 bytes). High dwords are don't-care.
	permdDwords = []uint32{0, 1, 2, 4, 5, 6, 7, 7}
)

func permdBytes() []byte {
	b := make([]byte, 32)
	for i, v := range permdDwords {
		b[i*4+0] = byte(v)
		b[i*4+1] = byte(v >> 8)
		b[i*4+2] = byte(v >> 16)
		b[i*4+3] = byte(v >> 24)
	}
	return b
}

func sig() abi.Signature {
	return abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		[]abi.Arg{abi.Scalar("ret", abi.Int64)},
	)
}

func main() {
	f := emit.NewFile("amd64")

	lutLo := f.Data("dlutLo", rep(lutLoBytes, 2))
	lutHi := f.Data("dlutHi", rep(lutHiBytes, 2))
	lutRoll := f.Data("dlutRoll", rep(lutRollBytes, 2))
	mul1 := f.Data("dmul1", rep(maddubsBytes, 16)) // 32 bytes
	mul2 := f.Data("dmul2", rep(maddwdWords, 8))   // 32 bytes
	pshuf := f.Data("dpshuf", rep(packShuf, 2))
	c0f := f.Data("dc0f", rep([]byte{0x0f}, 32))
	c2f := f.Data("dc2f", rep([]byte{0x2f}, 32))
	permd := f.Data("dpermd", permdBytes())

	// ---- SSSE3: 16 chars -> 12 bytes ----
	// Registers: X8=lutLo X9=lutHi X10=lutRoll X11=mul1 X12=mul2 X13=pshuf
	//            X14=0x0f mask  X15=0x2f
	s := amd64.NewFunc("decodeBlocksSSE", sig(), 0)
	s.LoadArg("dst_base", "DI").LoadArg("src_base", "SI").LoadArg("n", "CX").
		Raw("MOVOU %s+0(SB), X8", lutLo).
		Raw("MOVOU %s+0(SB), X9", lutHi).
		Raw("MOVOU %s+0(SB), X10", lutRoll).
		Raw("MOVOU %s+0(SB), X11", mul1).
		Raw("MOVOU %s+0(SB), X12", mul2).
		Raw("MOVOU %s+0(SB), X13", pshuf).
		Raw("MOVOU %s+0(SB), X14", c0f).
		Raw("MOVOU %s+0(SB), X15", c2f).
		Raw("XORQ AX, AX"). // group counter / return value
		Raw("TESTQ CX, CX").Raw("JZ sdone").
		Label("sloop").
		Raw("MOVOU (SI), X0"). // X0 = 16 ASCII chars
		// hi nibble = char>>4 (logical). No byte-shift; emulate with word shift + mask.
		Raw("MOVO X0, X1").Raw("PSRLW $4, X1").Raw("PAND X14, X1"). // X1 = hi nibble
		Raw("MOVO X0, X2").Raw("PAND X14, X2").                     // X2 = lo nibble
		// hi = pshufb(lutHi, hiNibble); lo = pshufb(lutLo, loNibble).
		Raw("MOVO X9, X3").Raw("PSHUFB X1, X3"). // X3 = hi
		Raw("MOVO X8, X4").Raw("PSHUFB X2, X4"). // X4 = lo
		// Validity: a char is valid iff (lo & hi) == 0 across the WHOLE vector.
		// PTEST sets ZF = ((X4 & X3) == 0); JNZ bails the block on any set bit
		// (the error bytes are not guaranteed to have bit 7 set, so PMOVMSKB would
		// miss them — PTEST tests all bits).
		Raw("PTEST X3, X4").Raw("JNZ sdone").
		// roll index = hiNibble + (char==0x2f ? -1 : 0).
		Raw("MOVO X0, X6").Raw("PCMPEQB X15, X6"). // X6 = 0xff where char=='/'
		Raw("PADDB X1, X6").                       // X6 = hiNibble + eq2f(-1 as 0xff)
		Raw("MOVO X10, X7").Raw("PSHUFB X6, X7").  // X7 = roll
		Raw("PADDB X7, X0").                       // X0 = 6-bit values
		// pack: maddubs then madd.
		Raw("PMADDUBSW X11, X0").
		Raw("PMADDWL X12, X0"). // Go asm name for SSE PMADDWD (0F F5)
		Raw("PSHUFB X13, X0").  // compact to low 12 bytes
		Raw("MOVOU X0, (DI)").  // writes 16, only low 12 meaningful (caller reserves room)
		Raw("ADDQ $16, SI").Raw("ADDQ $12, DI").Raw("INCQ AX").
		Raw("DECQ CX").Raw("JNZ sloop").
		Label("sdone").Raw("MOVQ AX, ret+56(FP)").Ret()
	f.Add(s.Func())

	// ---- AVX2: 32 chars -> 24 bytes ----
	// Y8=lutLo Y9=lutHi Y10=lutRoll Y11=mul1 Y12=mul2 Y13=pshuf Y14=0x0f
	// Y15=VPERMD cross-lane dword control. 0x2f is loaded from memory per iteration
	// (used once) to free a register for the permute control under AVX2 (only Y0-Y15
	// exist without AVX512).
	v := amd64.NewFunc("decodeBlocksAVX2", sig(), 0)
	v.LoadArg("dst_base", "DI").LoadArg("src_base", "SI").LoadArg("n", "CX").
		Raw("VMOVDQU %s+0(SB), Y8", lutLo).
		Raw("VMOVDQU %s+0(SB), Y9", lutHi).
		Raw("VMOVDQU %s+0(SB), Y10", lutRoll).
		Raw("VMOVDQU %s+0(SB), Y11", mul1).
		Raw("VMOVDQU %s+0(SB), Y12", mul2).
		Raw("VMOVDQU %s+0(SB), Y13", pshuf).
		Raw("VMOVDQU %s+0(SB), Y14", c0f).
		Raw("VMOVDQU %s+0(SB), Y15", permd).
		Raw("XORQ AX, AX").
		Raw("TESTQ CX, CX").Raw("JZ vdone").
		Label("vloop").
		Raw("VMOVDQU (SI), Y0").                           // 32 chars
		Raw("VPSRLW $4, Y0, Y1").Raw("VPAND Y14, Y1, Y1"). // hi nibble
		Raw("VPAND Y14, Y0, Y2").                          // lo nibble
		Raw("VPSHUFB Y1, Y9, Y3").                         // hi = pshufb(lutHi, hiNibble)
		Raw("VPSHUFB Y2, Y8, Y4").                         // lo = pshufb(lutLo, loNibble)
		// Validity: valid iff (lo & hi) == 0 over the whole vector. VPTEST sets
		// ZF = ((Y4 & Y3) == 0); JNZ bails on any set bit (error bytes are not
		// guaranteed to have bit 7 set, so VPMOVMSKB would miss them).
		Raw("VPTEST Y3, Y4").Raw("JNZ vdone").
		Raw("VPCMPEQB %s+0(SB), Y0, Y6", c2f).Raw("VPADDB Y1, Y6, Y6"). // roll index
		Raw("VPSHUFB Y6, Y10, Y7").                                     // roll
		Raw("VPADDB Y7, Y0, Y0").                                       // 6-bit values
		Raw("VPMADDUBSW Y11, Y0, Y0").
		Raw("VPMADDWD Y12, Y0, Y0").
		Raw("VPSHUFB Y13, Y0, Y0"). // compact within each 128-bit lane: low 12 of each
		// lane0's 12 valid bytes are in dwords 0..2, lane1's in dwords 4..6; VPERMD
		// pulls them together into output dwords 0..5 (24 contiguous bytes).
		Raw("VPERMD Y0, Y15, Y0").
		Raw("VMOVDQU Y0, (DI)"). // store 24 (writes 32, caller reserves room)
		Raw("ADDQ $32, SI").Raw("ADDQ $24, DI").Raw("INCQ AX").
		Raw("DECQ CX").Raw("JNZ vloop").
		Label("vdone").Raw("MOVQ AX, ret+56(FP)").Raw("VZEROUPPER").Ret()
	f.Add(v.Func())

	if err := os.WriteFile("decode_amd64.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote decode_amd64.s")
}
