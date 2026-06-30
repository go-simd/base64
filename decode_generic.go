//go:build !amd64 && !arm64 && !ppc64le && !s390x

package base64

// decodeSIMD has no SIMD decode kernel on this arch (riscv64, loong64, …); the
// whole input goes to the standard library. amd64/arm64/ppc64le/s390x each ship a
// real decode kernel (decode's pack is shift-only, so even arm64 — which lacks the
// vector integer multiply that keeps its encode pack from going wider — gets one).
// The url flag is irrelevant here — the wrapped encoding/base64 encoding already
// selects the alphabet.
func decodeSIMD(dst, src []byte, url bool) (srcDone, dstDone int) { return 0, 0 }
