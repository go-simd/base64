//go:build !amd64 && !arm64 && !ppc64le && !s390x

package base64

// On an arch with no SIMD kernel (riscv64, loong64, …) both dispatch paths must
// stay exercised by the test suite so coverage is exact: the *SIMDMin thresholds
// match the SSE-class kernels (16 to encode, 16+8 to decode) so short inputs take
// the stdlib short-circuit in Encode/Decode while larger ones fall through to the
// encodeSIMD/decodeSIMD stubs below — which report no progress, so the whole input
// goes to the standard library either way.
const (
	encodeSIMDMin = 16
	decodeSIMDMin = 16 + 8
)

// encodeSIMD has no SIMD kernel on this arch; the whole input goes to the
// standard library (the url flag is irrelevant here — the wrapped encoding/base64
// encoding already selects the alphabet).
func encodeSIMD(dst, src []byte, url bool) (srcDone, dstDone int) { return 0, 0 }
