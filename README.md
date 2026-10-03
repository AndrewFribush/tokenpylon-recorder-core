# TokenPylon recorder core

The streaming usage recorder extracted from [TokenPylon](https://tokenpylon.com), a price index and cost-analysis service for LLM APIs. This repository contains the Go proxy and event packages, their tests, and a synthetic local demo. It is a source showcase; the full collector and hosted service remain private.

The proxy forwards JSON and server-sent events while observing provider-reported token counts, cache usage, timing, status, and cancellation. OpenAI-compatible, Anthropic, and Google response formats have separate extractors. Missing usage stays `null`. Message bodies and authorization headers are absent from the event schema; request and response bytes are processed in memory.

## Run locally

Go 1.25 or newer is required. There are no third-party Go dependencies. With Go already installed, these commands disable dependency downloads and automatic toolchain installation:

```sh
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
go run ./cmd/demo
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```

The demo starts two temporary loopback HTTP listeners, makes four synthetic calls, prints responses and usage events, then exits. It checks JSON and streaming responses, cache normalization, unknown usage, cancellation, and exclusion of message content from events. It uses no API keys, real provider calls, runtime configuration, histories, uploads, or persistent storage. The Go toolchain may write its normal build cache.

## How it works

```text
synthetic client → loopback proxy → synthetic provider
                         └──────→ metadata event → stdout
```

`internal/proxy` handles forwarding, bounded asynchronous observation, usage extraction, route checks, and optional admission callbacks. `internal/event` defines nullable metadata, normalization, validation, identities, and quota observations. Event types retain their original schema; some fields support collector features outside this extraction.

The demo exposes only its synthetic provider's exact route and refuses client redirects and other destinations. The library itself retains real-provider routing code; embedding it elsewhere can make network requests. Unit tests use synthetic fixtures and loopback servers. The included fuzz target runs its seed corpus during ordinary tests; a fuzzing campaign is a separate check.

## Behavior and limits

- Eligible OpenAI streaming chat requests gain `stream_options.include_usage`; request forwarding is therefore not always byte-for-byte identical. Local attribution headers are stripped before forwarding.
- Observation is best-effort: bounded buffers can drop recording data without interrupting response forwarding. A compressed or unrecognized response can pass through with unknown usage.
- Usage is what a provider reports, not an independently measured invoice or local token count. The demo proves behavior against synthetic responses, not compatibility with every current provider API.
- This snapshot omits SQLite, the dashboard, transcript scanning, uploader, hosted planner, accounts, price catalogs, credentials, service installation, and release packaging. It is not the complete TokenPylon recorder distribution.

`PROVENANCE.json` records the original working-tree file hashes and extraction adaptations. Copied source is unchanged except for module import paths. No private Git history or runtime data is included.

Copyright 2026 Andrew Fribush. All rights reserved. See `NOTICE`; no open-source license is granted.
