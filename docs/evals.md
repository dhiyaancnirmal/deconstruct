# HAR compiler evals

Use this before adding more capture surface.

```sh
make build
./bin/deconstruct-eval -har path/to/service.har -out /tmp/deconstruct-eval
```

The report fails when:

- the HAR imports zero flows
- the compiler includes zero executable steps
- export generation fails
- `-replay` is enabled and the workflow does not replay successfully

Recommended real-service pass:

```sh
mkdir -p private-hars
./scripts/eval-hars.sh private-hars /tmp/deconstruct-real-evals
```

Drop `linear.har`, `notion.har`, `discord.har`, etc. into `private-hars/`. Keep that directory out of git.

Only use `-replay` against safe test accounts or local/staging URLs. The default eval mode is compile-only because real HARs often target production systems and may contain non-idempotent mutations.

For compiler debugging, inspect:

- `report.json` for counts, extracted keys, bound templates, and replay failures
- `typescript/src/workflow.ts` for the compiled DAG
- `openapi.json` for the external contract
