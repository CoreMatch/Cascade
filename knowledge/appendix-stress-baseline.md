# Appendix — Concurrent Session Binding Stress Baseline

- **Status**: appendix (not an ADR; regression baseline)

## What this is

Baseline numbers for `TestConcurrentSessionBindingStress`
(`internal/proxy/session_stress_test.go::TestConcurrentSessionBindingStress`),
the concurrent Preregister → RegisterPlayer → identity-bind stress test
added on 2026-11-05. The test drives 256 full client sessions through the
proxy at concurrency 32 against a fake plaintext backend (trust `open`,
contract v1.2.1 transport selection) and fails if any session is lost
(asserted bind count and backend-side completion count both equal 256).

Use these numbers as a regression reference: a materially worse run on
comparable hardware (order-of-magnitude, not percent-level) is a signal to
look for accidental serialization, goroutine leaks, or allocation bloat in
the session path.

## Baseline (2026-11-05)

Machine: Intel i5-11400F (6c/12t), linux/amd64, go1.26.4.

| Mode | Wall time | Throughput | Allocated | Mallocs | Peak goroutines |
| --- | ---: | ---: | ---: | ---: | ---: |
| plain | 46 ms | ~5510 sessions/s | 11149 KiB | 75460 | 208 |
| `-race` | 107 ms | ~2390 sessions/s | 17737 KiB | 82577 | 233 |

Both runs: 256/256 sessions bound and completed, zero failures. The
`-race` row is the regression-suite baseline (the suite runs with `-race`).

## Reproduction

```bash
go test ./internal/proxy/ -run TestConcurrentSessionBindingStress -v -count=1
go test ./internal/proxy/ -run TestConcurrentSessionBindingStress -race -v -count=1
```

The test logs the same `stress baseline:` lines via `t.Logf`.
