//go:build !amd64 && !arm64 && !ppc64le && !s390x

package base64

// encodeSIMD has no SIMD kernel on this arch; the whole input goes to the
// standard library (the url flag is irrelevant here — the wrapped encoding/base64
// encoding already selects the alphabet).
func encodeSIMD(dst, src []byte, url bool) (srcDone, dstDone int) { return 0, 0 }
