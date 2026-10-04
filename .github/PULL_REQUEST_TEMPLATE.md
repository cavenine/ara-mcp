## Problem and context

<!-- What problem does this solve? Which MCP tool, transport, or Ara API is involved? -->

## Linked issues

<!-- Fixes #123, Refs #123, or describe the issue above if no ticket exists. -->

## Changes

<!-- List concrete behavior changes and the affected components. -->

## Verification

<!-- For new/changed runtime behavior, give the exact RED command and expected
failure, GREEN result, and final checks. Docs-only changes use docs verification.
State checks not run and why. For live tests, name
the Ara version, platform, equipment, and operations. Keep mock results separate. -->

## Logging and observability

<!-- For runtime changes, describe relevant log fields, metrics/spans, diagnostic
behavior, and their verification. Link docs/observability.md criteria; say when a
signal is unaffected. Avoid including credentials or full sensitive payloads. -->

## Compatibility and risks

<!-- Tool schemas, Ara versions, settings, ownership, or hardware-visible changes. -->

## AI assistance

<!-- Name the agent/model used, or write "None - human-authored". -->

## Checklist

- [ ] I followed CONTRIBUTING.md and applicable AGENTS.md guidance.
- [ ] Format, vet, race-test, and build checks pass, or omissions are explained.
- [ ] Tests cover non-trivial changed behavior without requiring hardware by default.
- [ ] Runtime behavior has RED/GREEN evidence and retained regression tests.
- [ ] Applicable logging/observability requirements are implemented and tested.
- [ ] Documentation and changelog reflect the implemented behavior.
- [ ] I considered both transports and Ara control ownership where applicable.
- [ ] No credentials, generated binaries, or unrelated changes are included.
