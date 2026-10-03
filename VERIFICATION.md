# Local verification

Verified on 2026-10-03 with Go 1.27.1 on darwin/arm64. CI is configured for Go 1.25.0 on Ubuntu 24.04; that CI job has not run. No repository was initialized, committed, pushed, or published during preparation.

Every Go command below ran with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`. No dependency or toolchain download was needed.

| Command | Result |
| --- | --- |
| `go test -count=1 ./...` | Passed: event and proxy packages; demo has no separate test files. |
| `go test -race -count=1 ./...` | Passed with no race report. |
| `go vet ./...` | Passed. |
| `go run ./cmd/demo` | Passed all four synthetic cases. |
| `go vet ./cmd/demo` | Passed after making cancellation read a complete SSE line. |
| `go run -race ./cmd/demo` | Passed all four cases after that adjustment, with no race report. |
| `go list -m all` | Listed only this module; no third-party dependencies. |

The demo asserts forwarding of a synthetic response marker, disjoint uncached/cache token counts, unknown usage remaining nullable, cancellation producing an incomplete event, and absence of prompt/response markers from event JSON. Its client only dials the demo proxy; that proxy exposes only the synthetic provider's exact loopback route. Existing tests cover additional parser, streaming, attribution, admission, and network-boundary behavior. Only fuzz seeds ran; no extended fuzzing campaign or real-provider compatibility validation was performed.

All 24 copied Go files were checked against `PROVENANCE.json`. Reversing the module-import substitution reproduces each original SHA-256 hash. A source scan found no personal filesystem paths or credential-shaped values. Fixture literals inspected include synthetic model/request identifiers, `Bearer sk-test`, loopback addresses, and `https://example.com`. No environment files, ledgers, transcript files, release binaries, private Git history, or other project folders were copied. Pattern scans are supporting evidence, not a guarantee of exhaustive secret detection.
