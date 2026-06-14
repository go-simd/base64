//go:build ignore

// Command gen produces encode_s390x.s with go-asmgen: Lemire/Muła vectorised
// base64 encode for s390x using the z/Architecture vector facility. s390x is
// big-endian, so VL loads 16 bytes with lane 0 = lowest address and the
// fullword vector arithmetic (VESRLF, VN) operates on naturally big-endian words
// — which is exactly the byte order the right-shift extraction wants, so no
// little-endian fix-up is needed.
//
// Per 12 input bytes (loaded 16 at a time), a VPERM spread places each 24-bit
// group into a 32-bit lane as bytes [b0,b1,b2,0] (the VPERM control is the source
// byte index per result byte; verified position-dependently under qemu). The four
// 6-bit indices are then pulled out with right-shift-only extraction
// (VESRLF immediate shift + VN masks), landing directly in their output byte
// positions. The SSE range bucket is rebuilt with VMNLB (unsigned min) + VSB for
// the saturating sub and VCHLB (unsigned compare-greater) for the >25 test, then a
// VPERM offset-LUT maps each index to its ASCII byte. VST stores the 16 chars.
//
// The cross-lane shuffles (the spread VPERM and the LUT VPERM) are the only places
// big-endian lane numbering could bite; both are verified by encoding known groups
// under qemu (the FuzzEncode gate confirms byte-identical output to encoding/base64).
//
// Run: GOWORK=off go run encode_s390x_gen.go
package main

import (
	"fmt"
	"os"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/emit"
	"github.com/go-asmgen/asmgen/s390x"
)

// be4 repeats a 32-bit constant into all four lanes, stored big-endian (lowest
// address = most-significant byte) so the in-register fullword value equals v.
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

func repByte(x byte) []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = x
	}
	return b
}

func main() {
	f := emit.NewFile("s390x")

	// Spread control: result byte k = source byte spread[k]; per lane [b0,b1,b2,0].
	spread := []byte{0, 1, 2, 0, 3, 4, 5, 0, 6, 7, 8, 0, 9, 10, 11, 0}
	shuf := f.Data("s390Shuf", spread)
	// After the spread each lane's fullword is W = b0<<24 | b1<<16 | b2<<8. The four
	// 6-bit indices land in their output byte positions with right shifts:
	// byte0 = (W>>2)&0x3f000000, byte1 = (W>>4)&0x003f0000,
	// byte2 = (W>>6)&0x00003f00, byte3 = (W>>8)&0x0000003f.
	m0 := f.Data("s390M0", be4(0x3f000000))
	m1 := f.Data("s390M1", be4(0x003f0000))
	m2 := f.Data("s390M2", be4(0x00003f00))
	m3 := f.Data("s390M3", be4(0x0000003f))
	c51 := f.Data("s390C51", repByte(51))
	c25 := f.Data("s390C25", repByte(25))
	c1 := f.Data("s390C1", repByte(1))
	lut := f.Data("s390Lut", []byte{65, 71, 252, 252, 252, 252, 252, 252, 252, 252, 252, 252, 237, 240, 0, 0})

	sig := abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		nil,
	)

	g := s390x.NewFunc("encodeBlocks", sig, 0)
	g.LoadArg("dst_base", "R1").
		LoadArg("src_base", "R2").
		LoadArg("n", "R3").
		Raw("MOVD $%s(SB), R4", shuf).Raw("VL (R4), V16").
		Raw("MOVD $%s(SB), R4", m0).Raw("VL (R4), V17").
		Raw("MOVD $%s(SB), R4", m1).Raw("VL (R4), V18").
		Raw("MOVD $%s(SB), R4", m2).Raw("VL (R4), V19").
		Raw("MOVD $%s(SB), R4", m3).Raw("VL (R4), V20").
		Raw("MOVD $%s(SB), R4", c51).Raw("VL (R4), V21").
		Raw("MOVD $%s(SB), R4", c25).Raw("VL (R4), V22").
		Raw("MOVD $%s(SB), R4", c1).Raw("VL (R4), V23").
		Raw("MOVD $%s(SB), R4", lut).Raw("VL (R4), V24").
		Raw("CMPBEQ R3, $0, done").
		Label("loop").
		Raw("VL (R2), V0").
		Raw("VPERM V0, V0, V16, V0").
		Raw("VESRLF $2, V0, V1").Raw("VN V1, V17, V1").
		Raw("VESRLF $4, V0, V2").Raw("VN V2, V18, V2").
		Raw("VO V2, V1, V1").
		Raw("VESRLF $6, V0, V2").Raw("VN V2, V19, V2").
		Raw("VO V2, V1, V1").
		Raw("VESRLF $8, V0, V2").Raw("VN V2, V20, V2").
		Raw("VO V2, V1, V1").
		Raw("VMNLB V1, V21, V2").
		Raw("VSB V2, V1, V2").
		Raw("VCHLB V1, V22, V3").
		Raw("VN V3, V23, V3").
		Raw("VAB V2, V3, V2").
		Raw("VPERM V24, V24, V2, V2").
		Raw("VAB V1, V2, V2").
		Raw("VST V2, (R1)").
		Raw("ADD $12, R2").
		Raw("ADD $16, R1").
		Raw("ADD $-1, R3").
		Raw("CMPBNE R3, $0, loop").
		Label("done").
		Ret()
	f.Add(g.Func())

	if err := os.WriteFile("encode_s390x.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote encode_s390x.s")
}
