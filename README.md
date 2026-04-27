# deconstruct

Backend-first foundation for a local-first macOS workflow-to-API tool.

This repository currently scopes implementation to the backend:

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
- deterministic workflow draft/compiler for captured flow sequences
- workflow run storage and step-by-step replay execution
- auth-chain tracing for cookies, bearer tokens, CSRF sources, refresh endpoints, login checkpoints, and auth failures
- encrypted local auth vault and auth profile persistence with cookie-warming metadata
- generated TypeScript/Python projects, OpenAPI 3.1, Postman collections, and Dockerfiles
- local MCP HTTP endpoint for resources, tools, and prompts
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

By default, the daemon stores data under:

```text
~/Library/Application Support/deconstruct/
```

## Current milestone

This repo has the backend-only surface complete for local capture, inspection, replay, workflow compilation, auth tracing, export generation, MCP access, and protocol/fidelity metadata. It does not include a macOS UI, NetworkExtension system capture, or browser extension.
