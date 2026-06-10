//go:build ignore

// Command gen produces encode_amd64.s with go-asmgen: Lemire's vectorised base64
// encode. Per 12 input bytes (loaded as 16), PSHUFB spreads them so each 32-bit
// lane holds one 24-bit group, two multiplies (PMULHUW/PMULLW) with masks pull
// out the four 6-bit indices, and a PSHUFB offset-LUT maps each index to its
// ASCII byte. Constant tables come from emit.File.Data. Run: go run encode_gen.go
package main

import (
	"fmt"
	"os"

	"github.com/go-asmgen/asmgen/abi"
	"github.com/go-asmgen/asmgen/amd64"
	"github.com/go-asmgen/asmgen/emit"
)

func rep4(v [4]byte) []byte { // a 32-bit constant repeated to 16 bytes
	var b []byte
	for i := 0; i < 4; i++ {
		b = append(b, v[:]...)
	}
	return b
}
func rep16(x byte) []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = x
	}
	return b
}

func main() {
	f := emit.NewFile("amd64")
	shuf := f.Data("shuf", []byte{1, 0, 2, 1, 4, 3, 5, 4, 7, 6, 8, 7, 10, 9, 11, 10})
	mask1 := f.Data("mask1", rep4([4]byte{0x00, 0xfc, 0xc0, 0x0f})) // 0x0fc0fc00
	mulhi := f.Data("mulhi", rep4([4]byte{0x40, 0x00, 0x00, 0x04})) // 0x04000040
	mask2 := f.Data("mask2", rep4([4]byte{0xf0, 0x03, 0x3f, 0x00})) // 0x003f03f0
	mullo := f.Data("mullo", rep4([4]byte{0x10, 0x00, 0x00, 0x01})) // 0x01000010
	c51 := f.Data("c51", rep16(51))
	c25 := f.Data("c25", rep16(25))
	lut := f.Data("lut", []byte{65, 71, 252, 252, 252, 252, 252, 252, 252, 252, 252, 252, 237, 240, 0, 0})

	sig := abi.LayoutArgs(
		[]abi.Arg{abi.Slice("dst"), abi.Slice("src"), abi.Scalar("n", abi.Int64)},
		nil,
	)
	b := amd64.NewFunc("encodeBlocks", sig, 0)
	b.LoadArg("dst_base", "DI").
		LoadArg("src_base", "SI").
		LoadArg("n", "CX").
		Raw("MOVOU %s+0(SB), X7", shuf).
		Raw("MOVOU %s+0(SB), X8", mask1).
		Raw("MOVOU %s+0(SB), X9", mulhi).
		Raw("MOVOU %s+0(SB), X10", mask2).
		Raw("MOVOU %s+0(SB), X11", mullo).
		Raw("MOVOU %s+0(SB), X12", c51).
		Raw("MOVOU %s+0(SB), X13", c25).
		Raw("MOVOU %s+0(SB), X14", lut).
		Raw("TESTQ CX, CX").
		Raw("JZ done").
		Label("loop").
		Raw("MOVOU (SI), X0").
		Raw("PSHUFB X7, X0"). // spread 12 bytes into 4 lanes
		Raw("MOVO X0, X1").
		Raw("PAND X8, X1").
		Raw("PMULHUW X9, X1"). // t1
		Raw("MOVO X0, X2").
		Raw("PAND X10, X2").
		Raw("PMULLW X11, X2"). // t3
		Raw("POR X2, X1").     // indices (0..63 per byte)
		Raw("MOVO X1, X3").
		Raw("PSUBUSB X12, X3"). // subs(idx, 51)
		Raw("MOVO X1, X4").
		Raw("PCMPGTB X13, X4"). // idx > 25
		Raw("PSUBB X4, X3").    // bucket
		Raw("MOVO X14, X5").
		Raw("PSHUFB X3, X5"). // offsets = lut[bucket]
		Raw("PADDB X1, X5").  // ascii = idx + offset
		Raw("MOVOU X5, (DI)").
		Raw("ADDQ $12, SI").
		Raw("ADDQ $16, DI").
		Raw("DECQ CX").
		Raw("JNZ loop").
		Label("done").
		Ret()
	f.Add(b.Func())
	if err := os.WriteFile("encode_amd64.s", []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote encode_amd64.s")
}
