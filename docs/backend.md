# Backend Scope

The backend starts with durable local primitives before any agent layer:

1. Store captured flow metadata in SQLite.
2. Store bodies as content-addressed compressed blobs.
3. Expose local daemon state over JSON-RPC on a Unix domain socket.
4. Capture HTTP proxy requests.
5. Capture HTTPS requests through CONNECT using a generated local root CA.
6. Preserve and restore macOS system proxy settings through explicit JSON-RPC calls.
7. Leave macOS NetworkExtension capture as explicit future work.

## Daemon endpoints

The Unix socket JSON-RPC server accepts newline-delimited JSON-RPC 2.0 requests.

Initial methods:

- `daemon.status`
- `ca.info`
- `ca.trust`
- `ca.revoke_trust`
- `ca.rotate`
- `proxy.status`
- `proxy.start`
- `proxy.stop`
- `sessions.list`
- `flows.list`
- `flows.query`
- `flows.read`
- `flows.compare`
- `flows.export_curl`
- `flows.export_script`
- `workflows.export_project`
- `workflows.export_openapi`
- `workflows.export_postman`
- `exports.list`
- `replay.run`
- `replay.variations`
- `replay.runs.list`
- `replay.runs.read`
- `workflows.suggest`
- `workflows.save`
- `workflows.list`
- `workflows.read`
- `workflows.run`
- `workflow_runs.list`
- `auth.trace`
- `auth.chains.list`
- `auth.chains.read`
- `auth.profiles.save`
- `auth.profiles.list`
- `auth.profiles.read`
- `auth.vault.save`
- `fidelity.profiles.list`
- `fidelity.diagnose`
- `har.import`
- `apps.list`
- `hosts.list`
- `tags.list`
- `tags.add`
- `tags.remove`
- `filters.list`
- `filters.save`
- `filters.delete`
- `system_proxy.read`
- `system_proxy.enable`
- `system_proxy.disable`

## Proxy behavior

The first proxy implementation supports normal HTTP proxy requests where clients send absolute-form URLs.

`CONNECT` is intercepted when the client trusts the generated deconstruct root CA. The CA certificate path is returned by `daemon.status` and `ca.info`.

`ca.trust` and `ca.revoke_trust` call macOS `security` against the system keychain. They are explicit operations because they may prompt for administrator approval. `ca.rotate` replaces the local root CA and clears the daemon's generated leaf-certificate cache.

The daemon does not silently change system proxy settings. Clients can call `system_proxy.enable` and `system_proxy.disable` with a macOS network service name, for example `Wi-Fi`, when they want the backend to update and later restore system proxy state.

## Inspection APIs

The backend now exposes the data needed by a native traffic inspector:

- `flows.query` returns sortable/filterable flow grid rows.
- `flows.read` returns parsed headers, cookies, query params, raw bodies, and JSON bodies where applicable.
- `flows.compare` returns field-level differences across two or more flows.
- `apps.list`, `hosts.list`, and `tags.list` provide sidebar counts.
- `tags.add` and `tags.remove` manage flow tags.
- `filters.save`, `filters.list`, and `filters.delete` persist named flow-grid queries.

Explicit proxy captures do not yet have native app attribution, so current proxy flows use `Unknown` as the app name. Real bundle ID/PID attribution is reserved for the later macOS capture milestone.

## Protocol Parsing And Import

`flows.read` returns redacted inspector data by default. Parsed output currently includes:

- JSON body trees
- GraphQL operation name, query, and variables
- URL-encoded forms
- multipart part metadata
- SSE events
- gRPC/gRPC-web metadata
- best-effort protobuf wire field trees
- gzip, Brotli, and zstd decoded body views where headers expose `Content-Encoding`
- request and response cookies derived from redacted headers

`har.import` accepts either a file path or inline HAR JSON and imports HAR 1.2-style entries as a normal session. Imported flows are tagged with `imported_har`, plus protocol tags such as `graphql`, `form`, `multipart`, `compressed`, `websocket`, `sse`, `grpc`, `grpc_web`, and `protobuf` when detected. Secrets are redacted by default during import.

Generated cURL output is also redacted by default.

## Replay

Replay v1 can execute a captured request with optional edits:

- method override
- URL override
- query param replacement
- header add/replace/remove
- body replacement
- dry-run preparation without network execution
- retry on network errors or server errors
- A/B variation runs over different overrides
- replay profile selection through `profile` / `replay_profile`

Each replay execution is recorded as a `replay_runs` row and, when a response is received, as a normal replay flow in the `Replay runs` session. Replay flows are tagged with `replay_profile:<profile>`. Use `flows.compare` with the original flow id and replay flow id to inspect original-vs-replay differences.

Replay profiles:

- `standard`: Go HTTP client replay.
- `utls`: real HTTPS replay through uTLS with selectable ClientHello profiles via `profile_options.hello`, for example `chrome`, `firefox`, `safari`, `ios`, `android`, `edge`, or `random`.
- `curl_impersonate`: real external execution through a curl-impersonate-compatible binary. Use `profile_options.binary` to point at a bundled or installed executable.
- `browser`: real CDP browser replay. Use `profile_options.cdp_url`, defaulting to `http://127.0.0.1:9222`; the browser sends the request through `fetch` with `credentials: "include"`.
- `passthrough`: records a validation run without replaying the sensitive/pinned endpoint.

`flows.export_script` currently supports `typescript` and `python` one-request exports. Like cURL export, generated scripts redact obvious secrets by default.

## Workflow Compiler

The deterministic workflow compiler can draft a workflow from selected captured flow IDs:

- mutating requests are included as `primary_action`
- token/auth-looking paths are included as `auth_session`
- static assets are excluded
- simple JSON string fields become input schema candidates unless they look secret-bearing
- included steps get status assertions based on the original response
- simple JSON body inputs are represented as `${input.name}` templates in step replay overrides

`workflows.run` executes included steps sequentially through the replay engine and records a workflow run with per-step replay run IDs, statuses, and errors. Steps can receive input templating, auth profile material from the local vault, and step-level or run-level replay profiles.

## Auth Chain Tracing

`auth.trace` builds and persists a deterministic auth chain from selected flow IDs or a session/host query. The chain includes:

- cookie, bearer-token, session-token, CSRF-token, access-token, and refresh-token artifacts
- source flow, first/last seen timestamps, use sites, and storage classification for each artifact
- CSRF links from a source response/header/body to later request usage
- refresh endpoint candidates when token-looking paths issue token artifacts
- interactive checkpoints for login, SSO, MFA, and authorization markers
- auth failure classifications for 401, 403, login redirects, and login-page responses

`auth.profiles.save`, `auth.profiles.list`, and `auth.profiles.read` manage per-host auth profiles. Profiles can reference an auth chain, an encrypted secret bundle, and cookie-warming schedule metadata.

`auth.vault.save` encrypts session material with a local AES-GCM vault key under the daemon data directory and stores only ciphertext in SQLite. The RPC returns the bundle ID and updates the profile reference; it does not expose plaintext secrets.

`workflows.run` accepts `auth_profile_id`. When present, the daemon decrypts that profile's secret bundle locally and applies refreshed auth material as replay overrides. Supported bundle keys are `cookie`, `cookie.<name>`, `authorization`, CSRF header names, and `header.<Name>`. If the profile is marked `interactive_login_required` and has no bundle, the run returns an explicit interactive-login-required error.

Cookie warming runs inside `deconstructd` on the `--cookie-warming-check` interval. It scans enabled auth profiles, decrypts only the referenced local secret bundle, and runs the configured workflow through the normal workflow runner. Results are stored as workflow/replay runs.

## Generated Artifacts

`workflows.export_project` generates TypeScript or Python project files with workflow code, schema stubs, README, `.env.example`, Dockerfile, and `openapi.json`.

`workflows.export_openapi` emits OpenAPI 3.1 action endpoints with input/output schemas, examples, server URL, and secret-profile security metadata.

`workflows.export_postman` emits a Postman collection for included workflow steps. `exports.list` returns persisted export artifact metadata.

## MCP Server

The daemon serves a local MCP-compatible HTTP endpoint on `--mcp`, which defaults to `127.0.0.1:18081`.

Supported MCP resources:

- `deconstruct://sessions`
- `deconstruct://sessions/{id}`
- `deconstruct://flows/{id}`
- `deconstruct://workflows/{id}`
- `deconstruct://schemas/{id}`

Supported MCP tools:

- `search_flows`
- `read_flow`
- `read_sequence`
- `create_workflow`
- `run_workflow`
- `export_openapi`
- `export_script`

## Fidelity Diagnostics

`fidelity.profiles.list` exposes replay/capture profile metadata for standard replay, browser replay, uTLS, curl-impersonate, and passthrough validation. These profiles are backed by actual replay adapters; curl-impersonate requires an installed or bundled compatible binary.

`fidelity.diagnose` recommends a replay profile for a captured flow based on tags, auth-bearing headers, interactive auth markers, and protocol metadata. The diagnostic is explicit about visibility versus TLS/client-fingerprint fidelity.

## Verification

From the repository root:

```sh
make test
make build
make smoke
```

CI runs the same Go test/build path on macOS.
