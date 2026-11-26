# Contributing to tokensafe-backend

## Sign-off (DCO)

This project uses the **Developer Certificate of Origin 1.1** ([DCO](DCO)). Sign every
commit:

```bash
git commit -s -m "feat(go): ..."
```

There is no separate CLA to sign.

## Licence of contributions

- `go/`, `node/`: **Apache-2.0**.
- `python/`: **Apache-2.0** from 0.5.0 (pre-0.5.0 releases were MIT and stay MIT).

By contributing you agree your contribution is licensed under the same terms.

## Before opening a PR

```bash
cd go && go test ./...
cd python && pytest
cd node && pnpm install && pnpm build && pnpm test
```

- Adapter changes must pass the language-neutral fixtures (`fixtures/conformance-cases.json`,
  v5) and commit the regenerated reports under `certification/` (and
  `certification/transports/` for stdio).
- Regenerate with the commands in the README, then run
  `python3 scripts/check_certification.py` against a fresh directory and
  `python3 scripts/render_certification.py --check`.
- Never claim a capability or a transport that a report does not back — the certification
  block in the README is generated, and CI fails when it drifts.
