//go:build ignore

// Command gen produces encode_amd64.s with go-asmgen: Lemire's vectorised base64
// encode, both an SSE2/SSSE3 path (12 input bytes -> 16 chars per 128-bit block)
// and an AVX2 path (24 -> 32 per 256-bit block, VPERMD to cross-lane the input).
// Each: PSHUFB/VPSHUFB spread, two multiplies pull out the 6-bit indices, a
// PSHUFB offset-LUT maps each to its ASCII byte. Constant tables come from
// emit.File.Data. Run: go run encode_gen.go
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
func repByte(x byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = x
	}
	return b
}

var (
	shufBytes  = []byte{1, 0, 2, 1, 4, 3, 5, 4, 7, 6, 8, 7, 10, 9, 11, 10}
	mask1Bytes = []byte{0x00, 0xfc, 0xc0, 0x0f} // 0x0fc0fc00
	mulhiBytes = []byte{0x40, 0x00, 0x00, 0x04} // 0x04000040
	mask2Bytes = []byte{0xf0, 0x03, 0x3f, 0x00} // 0x003f03f0
	mulloBytes = []byte{0x10, 0x00, 0x00, 0x01} // 0x01000010
	lutBytes   = []byte{65, 71, 252, 252, 252, 252, 252, 252, 252, 252, 252, 252, 237, 240, 0, 0}
)

func sig() abi.Signature {
	return abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		nil,
	)
}

func main() {
	f := emit.NewFile("amd64")

	// ---- SSE2/SSSE3: 12 -> 16 ----
	shuf := f.Data("shuf", shufBytes)
	m1 := f.Data("mask1", rep(mask1Bytes, 4))
	mh := f.Data("mulhi", rep(mulhiBytes, 4))
	m2 := f.Data("mask2", rep(mask2Bytes, 4))
	ml := f.Data("mullo", rep(mulloBytes, 4))
	c51 := f.Data("c51", repByte(51, 16))
	c25 := f.Data("c25", repByte(25, 16))
	lut := f.Data("lut", lutBytes)

	s := amd64.NewFunc("encodeBlocksSSE", sig(), 0)
	s.LoadArg("dst_base", "DI").LoadArg("src_base", "SI").LoadArg("n", "CX").
		Raw("MOVOU %s+0(SB), X7", shuf).
		Raw("MOVOU %s+0(SB), X8", m1).
		Raw("MOVOU %s+0(SB), X9", mh).
		Raw("MOVOU %s+0(SB), X10", m2).
		Raw("MOVOU %s+0(SB), X11", ml).
		Raw("MOVOU %s+0(SB), X12", c51).
		Raw("MOVOU %s+0(SB), X13", c25).
		Raw("MOVOU %s+0(SB), X14", lut).
		Raw("TESTQ CX, CX").Raw("JZ sdone").
		Label("sloop").
		Raw("MOVOU (SI), X0").Raw("PSHUFB X7, X0").
		Raw("MOVO X0, X1").Raw("PAND X8, X1").Raw("PMULHUW X9, X1").
		Raw("MOVO X0, X2").Raw("PAND X10, X2").Raw("PMULLW X11, X2").
		Raw("POR X2, X1").
		Raw("MOVO X1, X3").Raw("PSUBUSB X12, X3").
		Raw("MOVO X1, X4").Raw("PCMPGTB X13, X4").
		Raw("PSUBB X4, X3").
		Raw("MOVO X14, X5").Raw("PSHUFB X3, X5").
		Raw("PADDB X1, X5").
		Raw("MOVOU X5, (DI)").
		Raw("ADDQ $12, SI").Raw("ADDQ $16, DI").Raw("DECQ CX").Raw("JNZ sloop").
		Label("sdone").Ret()
	f.Add(s.Func())

	// ---- AVX2: 24 -> 32 ----
	perm := f.Data("perm", []byte{0, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 3, 0, 0, 0, 4, 0, 0, 0, 5, 0, 0, 0, 6, 0, 0, 0})
	shuf2 := f.Data("shuf2", rep(shufBytes, 2))
	m1b := f.Data("mask1b", rep(mask1Bytes, 8))
	mhb := f.Data("mulhib", rep(mulhiBytes, 8))
	m2b := f.Data("mask2b", rep(mask2Bytes, 8))
	mlb := f.Data("mullob", rep(mulloBytes, 8))
	c51b := f.Data("c51b", repByte(51, 32))
	c25b := f.Data("c25b", repByte(25, 32))
	lutb := f.Data("lutb", rep(lutBytes, 2))

	vv := amd64.NewFunc("encodeBlocksAVX2", sig(), 0)
	vv.LoadArg("dst_base", "DI").LoadArg("src_base", "SI").LoadArg("n", "CX").
		Raw("VMOVDQU %s+0(SB), Y7", perm).
		Raw("VMOVDQU %s+0(SB), Y8", shuf2).
		Raw("VMOVDQU %s+0(SB), Y9", m1b).
		Raw("VMOVDQU %s+0(SB), Y10", mhb).
		Raw("VMOVDQU %s+0(SB), Y11", m2b).
		Raw("VMOVDQU %s+0(SB), Y12", mlb).
		Raw("VMOVDQU %s+0(SB), Y13", c51b).
		Raw("VMOVDQU %s+0(SB), Y14", c25b).
		Raw("VMOVDQU %s+0(SB), Y15", lutb).
		Raw("TESTQ CX, CX").Raw("JZ vdone").
		Label("vloop").
		Raw("VMOVDQU (SI), Y0").
		Raw("VPERMD Y0, Y7, Y0").  // cross-lane: lane0=bytes0-15, lane1=bytes12-27
		Raw("VPSHUFB Y8, Y0, Y0"). // per-lane spread
		Raw("VPAND Y9, Y0, Y1").Raw("VPMULHUW Y10, Y1, Y1").
		Raw("VPAND Y11, Y0, Y2").Raw("VPMULLW Y12, Y2, Y2").
		Raw("VPOR Y2, Y1, Y1").      // indices
		Raw("VPSUBUSB Y13, Y1, Y3"). // subs(idx,51)
		Raw("VPCMPGTB Y14, Y1, Y4"). // idx>25
		Raw("VPSUBB Y4, Y3, Y3").    // bucket
		Raw("VPSHUFB Y3, Y15, Y5").  // offsets = lut[bucket]
		Raw("VPADDB Y1, Y5, Y5").    // ascii
		Raw("VMOVDQU Y5, (DI)").
		Raw("ADDQ $24, SI").Raw("ADDQ $32, DI").Raw("DECQ CX").Raw("JNZ vloop").
		Label("vdone").Raw("VZEROUPPER").Ret()
	f.Add(vv.Func())

	if err := os.WriteFile("encode_amd64.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote encode_amd64.s")
}
