# Security Policy

## Supported versions

| Version | Support |
|---|---|
| 1.x | ✅ Receives security fixes |
| 0.0.x | ❌ Unsupported — left behind on the old module path, `github.com/mustafakarakulak/go-logging` |

## Reporting a vulnerability

If you find a security issue, **do not open a public issue.** Use GitHub's
private advisory flow instead:

**[Security → Report a vulnerability](https://github.com/mustafakarakulak/gophlog/security/advisories/new)**

Only you and the repository owner can see that report, and the details stay
private until a fix ships.

Your report is easier to act on if it includes:

- The affected version and package (`gophlog`, `middleware`, `httpclient`)
- The smallest snippet or test that triggers the problem
- The expected and the observed log output
- Your assessment of the impact: which data is exposed, under what conditions

### Response times

- **72 hours** to acknowledge the report
- **7 days** for an initial assessment (is it valid, how severe)
- When the fix ships, the advisory is published, with credit if you want it

This is a one-person open-source project, so treat these as targets rather than
guarantees.

## Classes that matter for this library

Part of gophlog's job is masking sensitive data before it reaches a log. The
behaviours below therefore count as vulnerabilities, not ordinary bugs:

- **Masking that leaks** — a `MaskingStrategy` emitting the whole value it was
  supposed to hide, or a distinguishing part of it. Masking is fail-closed by
  design: if a value is too short for the strategy to hide anything, the value
  is masked in full.
- **Masking that is bypassed** — a field marked through the `mask` struct tag or
  `MaskFieldStrategies` passing through unmasked on some code path (nested
  struct, embedded field, `map`, error path, curl dump).
- **Credential leaks** — headers such as `Authorization`, `Cookie` or
  `X-Api-Key`, or credentials in a request body, written in plain text to the
  `LogCurl` output or to the log line.
- **Log injection** — user input escaping the JSON output and forging a log
  line of its own.
- **Input-driven resource exhaustion** — a crafted request body, header or
  payload causing unbounded allocation or an infinite loop.

## Out of scope

- **`Fatal` does not terminate the process** — a deliberate design decision,
  documented in the README.
- **Forgetting to mask data you log** — the library cannot know which of your
  fields are sensitive; masking is opt-in through configuration.
- **Packages under `internal/`** — outside the semver guarantee. A leak there is
  still a valid report if it can be triggered through an exported code path.
- **Dependency vulnerabilities** — this library has no external dependencies.
  For standard-library advisories, update your Go toolchain; CI runs
  `govulncheck` weekly.
