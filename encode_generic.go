//go:build !amd64 && !arm64 && !ppc64le && !s390x

package base64

// On an arch with no SIMD kernel (riscv64, loong64, …) the *SIMDMin thresholds
// are 0, so Encode/Decode never take the short-circuit and always fall through to
// encodeSIMD/decodeSIMD, which report no progress; the whole input goes to the
// standard library. (Keeping the thresholds at 0 — rather than "infinity" — leaves
// these stubs on the executed path so coverage stays exact.)
const (
	encodeSIMDMin = 0
	decodeSIMDMin = 0
)

// encodeSIMD has no SIMD kernel on this arch; the whole input goes to the
// standard library (the url flag is irrelevant here — the wrapped encoding/base64
// encoding already selects the alphabet).
func encodeSIMD(dst, src []byte, url bool) (srcDone, dstDone int) { return 0, 0 }
