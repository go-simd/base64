//go:build ppc64le

package base64

import (
	"bytes"
	stdb64 "encoding/base64"
	"math/rand"
	"testing"

	"golang.org/x/sys/cpu"
)

// TestDispatchPPC64LE drives both ppc64le dispatch branches for encode and
// decode: the VSX kernel and the stdlib scalar fallback. The kernels emit ISA-3.0
// (POWER9) instructions (LXVB16X/STXVB16X) that raise SIGILL on POWER8, so the
// kernel-forcing branch runs only when the host is actually POWER9+ (mirroring the
// amd64 force tests). The scalar-fallback branch (hasVSX=false) is always
// exercised. The power9-targeted QEMU CI job and the native POWER9/POWER10 farm
// runs cover the kernel branch; on a POWER8 farm node only the fallback runs (and
// must not SIGILL). Every case stays byte- and error-identical to encoding/base64.
func TestDispatchPPC64LE(t *testing.T) {
	saved := hasVSX
	defer func() { hasVSX = saved }()

	rng := rand.New(rand.NewSource(17))
	cases := func() []string {
		var cs []string
		for _, n := range []int{0, 1, 5, 12, 15, 16, 17, 23, 24, 25, 48, 96, 200, 1000} {
			src := make([]byte, n)
			rng.Read(src)
			cs = append(cs, stdb64.StdEncoding.EncodeToString(src))
		}
		// Invalid bytes deep inside clean blocks so a wrong bail offset diverges.
		clean := stdb64.StdEncoding.EncodeToString(make([]byte, 120))
		for _, off := range []int{0, 15, 16, 31, 33, 50} {
			b := []byte(clean)
			b[off] = '!'
			cs = append(cs, string(b))
		}
		return cs
	}()

	check := func(tag string) {
		t.Helper()
		for _, in := range cases {
			gotB, gotErr := DecodeString(in)
			wantB, wantErr := stdb64.StdEncoding.DecodeString(in)
			if !bytes.Equal(gotB, wantB) || !errEqual(gotErr, wantErr) {
				t.Fatalf("%s DecodeString(%q):\n got=%x err=%v\nwant=%x err=%v",
					tag, in, gotB, gotErr, wantB, wantErr)
			}
			// Round-trip the decoded bytes back through Encode.
			if gotErr == nil {
				enc := EncodeToString(wantB)
				if want := stdb64.StdEncoding.EncodeToString(wantB); enc != want {
					t.Fatalf("%s EncodeToString:\n got=%q\nwant=%q", tag, enc, want)
				}
			}
		}
	}

	// Scalar fallback: always safe on every ppc64le host (no VSX instructions).
	hasVSX = false
	check("fallback")

	// VSX kernel: force it on only when the CPU is POWER9+, otherwise the
	// LXVB16X/STXVB16X in the kernel would SIGILL (e.g. on a POWER8 farm node).
	if !cpu.PPC64.IsPOWER9 {
		t.Log("CPU is pre-POWER9; VSX kernel branch not exercised on this host")
		return
	}
	hasVSX = true
	check("vsx")
}
