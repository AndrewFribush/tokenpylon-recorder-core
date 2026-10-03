# TokenPylon recorder core

I built the recorder behind [TokenPylon](https://tokenpylon.com) to account for LLM calls that stream, stop halfway through, or return no usable token counts. The proxy forwards JSON and server-sent events while recording provider-reported token counts, cache usage, timing, status, and cancellation. Missing usage stays `null`, even when zero would look nicer on a chart.

TokenPylon is a price index and cost-analysis service for LLM APIs. This repository contains the Go proxy and event packages, their tests, and a synthetic local demo. The full collector and hosted service remain private.

## Recording without blocking the stream

`internal/proxy` forwards responses and observes them asynchronously through bounded buffers. If observation falls behind, it can drop recording data without interrupting forwarding. OpenAI-compatible, Anthropic, and Google formats have separate usage extractors. The package also handles route checks and optional admission callbacks.

`internal/event` defines nullable metadata, normalization, validation, identities, and quota observations. Message bodies and authorization headers are absent from the event schema; request and response bytes are processed in memory. The event types retain their original schema, including fields for collector features outside this extraction.

Unit tests, race tests, vet, and the four-case demo passed in local verification. The tests exercise parser and streaming behavior, attribution, admission, and network boundaries with synthetic fixtures and loopback servers. The demo checks response forwarding, cache normalization, unknown usage, cancellation, and exclusion of message content from events. `VERIFICATION.md` records the commands and environment. The included fuzz target runs its seed corpus during ordinary tests; an extended fuzzing campaign is a separate check.

## Run locally

The demo uses this path:

```text
synthetic client → loopback proxy → synthetic provider
                         └──────→ metadata event → stdout
```

Go 1.25 or newer is required. There are no third-party Go dependencies. With Go already installed, these commands disable dependency downloads and automatic toolchain installation:

```sh
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
go run ./cmd/demo
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```

The demo starts two temporary loopback HTTP listeners, makes four synthetic calls, prints responses and usage events, then exits. It exposes only its synthetic provider's exact route and refuses client redirects and other destinations. It uses no API keys, real provider calls, runtime configuration, histories, uploads, or persistent storage. The Go toolchain may write its normal build cache. The library retains real-provider routing code, so embedding it elsewhere can make network requests.

## Behavior and limits

- Eligible OpenAI streaming chat requests gain `stream_options.include_usage`; request forwarding is therefore not always byte-for-byte identical. Local attribution headers are stripped before forwarding.
- Observation is best-effort. A compressed or unrecognized response can pass through with unknown usage.
- Usage is what a provider reports, not an independently measured invoice or local token count. The demo proves behavior against synthetic responses, not compatibility with every current provider API.
- This snapshot omits SQLite, the dashboard, transcript scanning, uploader, hosted planner, accounts, price catalogs, credentials, service installation, and release packaging. It is not the complete TokenPylon recorder distribution.

`PROVENANCE.json` records the original working-tree file hashes and extraction adaptations. Copied source is unchanged except for module import paths. [Development history](HISTORY.md) preserves the original changes to these 24 Go files. Runtime data and history outside those files are excluded.

Copyright 2026 Andrew Fribush. All rights reserved. See `NOTICE`; no open-source license is granted.
