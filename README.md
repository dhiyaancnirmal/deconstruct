# deconstruct

Backend-first workflow compiler for turning captured HTTP traffic into replayable automations.

This repository currently scopes implementation to the backend. The highest-priority path is the compiler:

```text
HAR or capture dump -> workflow DAG -> TypeScript/Python/OpenAPI/Postman/MCP-ready artifacts
```

The proxy exists as an input path, not the product center.

Current backend pieces:

- `deconstructd`: local daemon
- SQLite metadata store
- content-addressed blob store
- JSON-RPC over Unix domain sockets
- explicit HTTP/HTTPS proxy capture foundation
- persisted local root CA for HTTPS CONNECT interception
- macOS system proxy enable/restore helpers
- backend inspection APIs for flow grids, filters, tags, hosts, apps, and compare
- protocol parsing for JSON, GraphQL, forms, multipart metadata, and compressed bodies
- HAR 1.2 import into normal deconstruct sessions
- default redaction for inspector and generated cURL output
- replay engine for captured requests with edits, retry, dry-run, variations, and replay history
- one-request cURL, TypeScript, and Python exports
- deterministic workflow draft/compiler for captured flow sequences, including response-to-request bindings
- workflow run storage and step-by-step replay execution
- auth-chain tracing for cookies, bearer tokens, CSRF sources, refresh endpoints, login checkpoints, and auth failures
- encrypted local auth vault and auth profile persistence with cookie-warming metadata
- generated TypeScript/Python projects, OpenAPI 3.1, Postman collections, Dockerfiles, and local API wrappers
- local MCP HTTP endpoint for resources, tools, and prompts
- real replay adapters for standard HTTP, uTLS, CDP browser fetch, curl-impersonate executables, and passthrough validation
- replay fidelity profiles and diagnostics
- SSE, gRPC/gRPC-web, and protobuf inspection tags
- `.deconstruct` bundle/schema naming

UI, NetworkExtension, browser extension, and agent surfaces are intentionally out of scope for this first pass.

## Backend layout

```text
daemon/
  cmd/deconstructd/
  internal/auth/
  internal/blobstore/
  internal/config/
  internal/jsonrpc/
  internal/mcp/
  internal/proxy/
  internal/store/
schemas/deconstruct/
docs/
```

## Run

```sh
make test
make build
make smoke
```

## Compiler eval

The important test is HAR in -> workflow DAG -> generated artifacts -> optional replay. The repo includes a CLI for that:

```sh
make build
./bin/deconstruct-eval \
  -har tests/fixtures/hars/csrf-create-item.har \
  -out /tmp/deconstruct-csrf-eval
```

That writes:

```text
/tmp/deconstruct-csrf-eval/
  report.json
  openapi.json
  postman.json
  typescript/
  python/
```

For live or staging services, add `-replay` only when the captured URLs are safe to call again. The eval runner imports the HAR without redaction by default because the compiler needs local token values to infer bindings; generated exports still redact auth material.

Batch real HARs with:

```sh
mkdir -p private-hars
./scripts/eval-hars.sh private-hars /tmp/deconstruct-real-evals
```

By default, the daemon stores data under:

```text
~/Library/Application Support/deconstruct/
```

## Current Proof

The checked-in smoke fixture proves:

- HAR import preserves a noisy static request, CSRF bootstrap request, and mutating action request.
- The compiler excludes the static request.
- The compiler extracts the CSRF value from the bootstrap response.
- The compiler binds that extracted value into the later request header/body.
- TypeScript generation emits a workflow-shaped client instead of a flat request dump.
- The generated TypeScript project loads under its own `npm test`.

This is not a substitute for real HARs from Linear, Notion, Discord, Stripe, etc. The next useful work is running `./scripts/eval-hars.sh` on those private captures and fixing the first failure.

## Current Milestone

This repo is now centered on proving the compiler path. The proxy remains available, but the repo-level smoke test exercises HAR import, workflow compilation, and artifact generation. It does not include a macOS UI, NetworkExtension system capture, or browser extension.
