# Contributing

Thanks for your interest in gridbase. Bug reports, fixes and documentation improvements are welcome. For anything larger than a small fix, please open an issue first so we can agree on the approach.

For security problems, do not open an issue: see [SECURITY.md](./SECURITY.md).

## Layout

| Path | What |
|---|---|
| `tunnel/` | Go tunnel server and client |
| `web/` | Admin console (Next.js 14, App Router; UI text is Japanese only) |
| `directus/` | Schema snapshots and the idempotent bootstrap (`init/init.mjs`) |
| `tests/e2e/` | End-to-end smoke test against the running compose stack |

## Run the checks

```sh
(cd tunnel && go vet ./... && go test ./...)
(cd web && npm ci && npm run lint && npx tsc --noEmit && npm test && npm run build)
(cd directus/init && npm ci) && node --test directus/init/*.test.mjs

cp .env.example .env
docker compose up -d --build
node tests/e2e/smoke.mjs
docker compose down -v && rm .env
```

CI runs the same steps (`.github/workflows/ci.yml`). A change is ready when all of them pass.

## Guidelines

- Keep changes small and focused; do not mix refactoring with behaviour changes.
- Add or update a test for behaviour you change (unit test next to the code, or a case in `tests/e2e/smoke.mjs` for cross-service behaviour). Do not weaken or skip existing tests to make a change pass.
- Security-sensitive code (authentication, authorization, CSRF checks, secret handling, `directus/init`) needs a test for the failure case, not only the happy path.
- Never commit real secrets, tokens, certificates or `.env` files. Example values must look like placeholders (`change-me`, `local-dev-`, `example.com`).
- `web/package-lock.json` must be generated with the npm that the Docker image uses (Node 22 / npm 10), otherwise `npm ci` in the image fails: `docker run --rm -v "$PWD/web":/app -w /app node:22-alpine npm install --package-lock-only`. The same goes for `directus/init/package-lock.json` (use `-v "$PWD/directus/init":/app`).
- The "exposed setup" test (bind address, https, real domain) and the default-password test exist twice, in `directus/init/secrets-check.mjs` and `web/scripts/deployment-check.mjs`, because the two images are built separately. Change both; `directus/init/parity.test.mjs` fails when they differ.
- Language: the console UI and its error messages are Japanese. Code comments are mostly Japanese in `web/` and English in `tunnel/`; documentation is English with a Japanese README (`README.ja.md`) that should stay in step with `README.md`. New files may use either; prefer English for new public documentation.
- If you change the schema, update both `directus/schema/gridbase.json` and `gridbase.postgres.json`, and check that a second `docker compose run --rm directus-init` reports "変更なし".

## License

By contributing you agree that your contributions are licensed under the [MIT License](./LICENSE).
