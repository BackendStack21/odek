# Runtime evaluation harness

`make eval` runs `cmd/odek-eval`, a deterministic harness around the
production `internal/loop.Engine`. It starts an OpenAI-compatible provider on
localhost, feeds scripted assistant replies, and exposes small stateful
fixture tools. No credentials, external network, or live model is used.

Each scenario keeps its model messages separate from its oracle. The oracle
checks fixture state and observed tool calls, so an assistant saying “done”
cannot by itself make a task pass. The JSON report contains per-case scenario
status, independently determined `task_success`, tool calls, synthetic
scripted-provider token counts, and elapsed milliseconds. `false_completion_rate` is the fraction of
cases whose oracle explicitly marked a success claim while required fixture
state was absent; it is a regression signal, not a model-quality score.
`cost_known` is always false for the shipped harness; it does not configure
prices or estimate cost.

The initial suite covers verified artifact work, a failed read followed by a
false success claim, unrelated reads after a write, transient failure and
recovery, cancellation, and plan acceptance checks for success, failed
evidence, missing evidence, and incremental revision behavior. Negative cases can still be scenario passes
when the oracle correctly records that the task did not succeed. The plan
cases use the production `plan` tool and `PlanStore`, including the runtime
incomplete marker for failed or missing evidence.

The reusable `internal/eval.RunWithOptions` API accepts a per-case client
factory. An application may use that adapter to compare a separately
authorized real provider, but it must provide its own credentials, network
policy, and model-message adapter. The shipped CLI intentionally does not
evaluate live-model intelligence. Cost reporting stays unknown because the
harness does not configure token prices.

## Running and extending the suite

```bash
make eval
# Save only the JSON report (without make's command echo):
go run ./cmd/odek-eval > eval-report.json
```

Exit status is 0 when every scenario passes, 1 for scenario failures, and 2
if the report cannot be encoded. The eleven-case baseline includes a deliberate
unguarded false-success control: all scenarios pass while
`false_completion_rate` is 1/11 (about 0.091). That expected control is not a failure of
the checked-plan guard or a live-model benchmark.

Add a case to `internal/eval.Scenarios` with fresh fixture state, scripted
responses, tools, and an independent oracle. Assert the required state and
observed tool outcomes, including expected failures; do not accept a success
claim as proof. `task_success` answers whether the fixture task was completed;
`scenario_passed` answers whether the runtime behaved as the test expected.
Add regression assertions in `internal/eval/eval_test.go`, then run:

```bash
go test -count=1 -timeout=120s ./internal/eval ./internal/loop
```
