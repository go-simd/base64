# Performance parity — go-simd/base64 vs stdlib / reference

**Methodology.** Apple M4 Max (arm64, NEON), macOS (Darwin 25.5.0), Go 1.26.4,
single core. References: `encoding/base64` (Go stdlib — also the scalar tail of
go-simd), `github.com/emmansun/base64 v0.6.2` (pure-Go SIMD, arm64 NEON),
`github.com/cristalhq/base64 v0.1.2` (pure-Go scalar "Turbo" — no assembly).
Inputs: pseudo-random bytes (seed 2), sizes 64 B / 1 KiB / 16 KiB / 1 MiB;
`-benchtime=0.3s -count=3`, median reported. Throughput is over the **source**
domain (raw bytes for encode, base64 text for decode). Correctness:
`go test` round-trips and byte-/error-matches `encoding/base64` on every size
before benchmarking. Reproduce:

```
GOWORK=off go test -run='^$' -bench=Parity -benchmem -benchtime=0.3s -count=3 .
```

`go-simd/base64` has NEON kernels for **encode and decode** on arm64, so these
are real SIMD numbers (not a fallback).

## Encode

| op | size | go-simd (GB/s) | scalar (stdlib) | emmansun (NEON ref) | cristalhq (scalar ref) | speedup vs stdlib | ratio vs emmansun | verdict |
|----|------|---------------:|----------------:|--------------------:|-----------------------:|------------------:|------------------:|---------|
| encode | 64 B   |  5.49 | 2.56 |  7.96 | 6.09 | 2.14× | 0.69× | beats stdlib; trails NEON ref at tiny sizes |
| encode | 1 KiB  | 20.7  | 2.85 | 22.7  | 5.85 | 7.27× | 0.91× | beats stdlib ~7×, near-parity with NEON ref |
| encode | 16 KiB | 22.5  | 2.92 | 23.2  | 5.81 | 7.71× | 0.97× | parity with NEON ref |
| encode | 1 MiB  | 23.0  | 2.89 | 23.0  | 5.52 | 7.96× | 1.00× | **parity with NEON ref**, ~8× stdlib |

## Decode

| op | size | go-simd (GB/s) | scalar (stdlib) | emmansun (NEON ref) | speedup vs stdlib | ratio vs emmansun | verdict |
|----|------|---------------:|----------------:|--------------------:|------------------:|------------------:|---------|
| decode | 64 B   | 5.74 | 3.68 |  6.42 | 1.56× | 0.89× | beats stdlib; near NEON ref |
| decode | 1 KiB  | 8.49 | 4.71 | 15.97 | 1.80× | 0.53× | beats stdlib; **trails NEON ref ~1.9×** |
| decode | 16 KiB | 8.51 | 4.87 | 16.19 | 1.75× | 0.53× | beats stdlib; **trails NEON ref ~1.9×** |
| decode | 1 MiB  | 8.52 | 4.85 | 16.27 | 1.76× | 0.52× | beats stdlib; **trails NEON ref ~1.9×** |

## Summary

* **Encode beats stdlib ~8×** at ≥1 KiB and reaches **parity with the
  best-in-class NEON reference (emmansun)** at 16 KiB+ (0.97–1.00×). It is ~4×
  faster than the scalar Turbo reference (cristalhq).
* **Decode beats stdlib ~1.75×** but **lags emmansun ~1.9×** at all medium/large
  sizes — emmansun sustains ~16 GB/s, go-simd ~8.5 GB/s. This is the clear gap.
* Zero allocations on both encode and decode for all sizes.

### Action items
1. **Decode kernel is the priority.** The NEON decode reaches only half of the
   reference. Investigate emmansun's NEON lookup/pack sequence and regenerate the
   go-asmgen kernel (better gather/shuffle for the 6→8 bit unpack; the decode
   validate+pack appears to be the bottleneck).
2. **amd64/AVX2 follow-up:** not measurable on this arm64 host. Run the same
   harness on a real x86_64 VM (Rosetta lacks AVX2; docker-qemu-user crashes Go)
   to confirm the AVX2 encode/decode parity vs emmansun AVX2/AVX-512.
3. Tiny-input (64 B) encode trails the NEON ref by ~1.4× — fixed dispatch/tail
   overhead dominates; a shorter SIMD-eligible threshold could help.
