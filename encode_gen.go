//go:build ignore

// Command gen produces encode_amd64.s with go-asmgen: Lemire's vectorised base64
// encode, both an SSE2/SSSE3 path (12 input bytes -> 16 chars per 128-bit block)
// and a 2x-unrolled AVX2 path (24 -> 32 per 256-bit block). The AVX2 input is
// placed lane0=bytes0-15, lane1=bytes12-27 with VMOVDQU + VINSERTI128 (no
// cross-lane VPERMD, which would bottleneck the shuffle port). Each: PSHUFB spread,
// two multiplies pull out the 6-bit indices, a PSHUFB offset-LUT maps each to its
// ASCII byte. Constant tables come from emit.File.Data. Run: go run encode_gen.go
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

	// ---- AVX2: 24 -> 32, 2x-unrolled, VPERMD-free ----
	shuf2 := f.Data("shuf2", rep(shufBytes, 2))
	m1b := f.Data("mask1b", rep(mask1Bytes, 8))
	mhb := f.Data("mulhib", rep(mulhiBytes, 8))
	m2b := f.Data("mask2b", rep(mask2Bytes, 8))
	mlb := f.Data("mullob", rep(mulloBytes, 8))
	c51b := f.Data("c51b", repByte(51, 32))
	c25b := f.Data("c25b", repByte(25, 32))
	lutb := f.Data("lutb", rep(lutBytes, 2))

	// VMOVDQU + VINSERTI128 place lane0=bytes0-15, lane1=bytes12-27 without VPERMD.
	// Dropping perm frees a register, so all eight constants stay in YMM (Y8-Y15);
	// two independent blocks (A=Y0-2, B=Y3-5) interleave per iteration for ILP.
	vv := amd64.NewFunc("encodeBlocksAVX2", sig(), 0)
	vv.LoadArg("dst_base", "DI").LoadArg("src_base", "SI").LoadArg("n", "CX").
		Raw("VMOVDQU %s+0(SB), Y8", shuf2).
		Raw("VMOVDQU %s+0(SB), Y9", m1b).
		Raw("VMOVDQU %s+0(SB), Y10", mhb).
		Raw("VMOVDQU %s+0(SB), Y11", m2b).
		Raw("VMOVDQU %s+0(SB), Y12", mlb).
		Raw("VMOVDQU %s+0(SB), Y13", c51b).
		Raw("VMOVDQU %s+0(SB), Y14", c25b).
		Raw("VMOVDQU %s+0(SB), Y15", lutb).
		Label("vpair").
		Raw("CMPQ CX, $2").Raw("JLT vsingle").
		Raw("VMOVDQU (SI), Y0").Raw("VINSERTI128 $1, 12(SI), Y0, Y0").
		Raw("VMOVDQU 24(SI), Y3").Raw("VINSERTI128 $1, 36(SI), Y3, Y3").
		Raw("VPSHUFB Y8, Y0, Y0").Raw("VPSHUFB Y8, Y3, Y3").
		Raw("VPAND Y9, Y0, Y1").Raw("VPMULHUW Y10, Y1, Y1").
		Raw("VPAND Y9, Y3, Y4").Raw("VPMULHUW Y10, Y4, Y4").
		Raw("VPAND Y11, Y0, Y2").Raw("VPMULLW Y12, Y2, Y2").
		Raw("VPAND Y11, Y3, Y5").Raw("VPMULLW Y12, Y5, Y5").
		Raw("VPOR Y2, Y1, Y1").Raw("VPOR Y5, Y4, Y4"). // idxA, idxB
		Raw("VPSUBUSB Y13, Y1, Y0").Raw("VPSUBUSB Y13, Y4, Y3").
		Raw("VPCMPGTB Y14, Y1, Y2").Raw("VPCMPGTB Y14, Y4, Y5").
		Raw("VPSUBB Y2, Y0, Y0").Raw("VPSUBB Y5, Y3, Y3"). // bucketA, bucketB
		Raw("VPSHUFB Y0, Y15, Y0").Raw("VPSHUFB Y3, Y15, Y3").
		Raw("VPADDB Y1, Y0, Y1").Raw("VPADDB Y4, Y3, Y4"). // asciiA, asciiB
		Raw("VMOVDQU Y1, (DI)").Raw("VMOVDQU Y4, 32(DI)").
		Raw("ADDQ $48, SI").Raw("ADDQ $64, DI").Raw("SUBQ $2, CX").Raw("JMP vpair").
		Label("vsingle").
		Raw("TESTQ CX, CX").Raw("JZ vdone").
		Raw("VMOVDQU (SI), Y0").Raw("VINSERTI128 $1, 12(SI), Y0, Y0").
		Raw("VPSHUFB Y8, Y0, Y0").
		Raw("VPAND Y9, Y0, Y1").Raw("VPMULHUW Y10, Y1, Y1").
		Raw("VPAND Y11, Y0, Y2").Raw("VPMULLW Y12, Y2, Y2").
		Raw("VPOR Y2, Y1, Y1").
		Raw("VPSUBUSB Y13, Y1, Y3").
		Raw("VPCMPGTB Y14, Y1, Y4").
		Raw("VPSUBB Y4, Y3, Y3").
		Raw("VPSHUFB Y3, Y15, Y5").
		Raw("VPADDB Y1, Y5, Y5").
		Raw("VMOVDQU Y5, (DI)").
		Label("vdone").Raw("VZEROUPPER").Ret()
	f.Add(vv.Func())

	if err := os.WriteFile("encode_amd64.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote encode_amd64.s")
}
