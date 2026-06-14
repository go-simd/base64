//go:build !amd64 && !arm64 && !ppc64le && !s390x

package base64

// encodeSIMD has no SIMD kernel on this arch; the whole input goes to the
// standard library.
func encodeSIMD(dst, src []byte) (srcDone, dstDone int) { return 0, 0 }
