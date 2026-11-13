package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// runFunctionalGate runs the visible checks, permits one repair on a test
// failure, and sends only functionally accepted changes to static review.
func (a *Agent) runFunctionalGate(ctx context.Context, step int, options RunOptions, terminal bool) gateResult {
	contract := options.Contract
	result := gateResult{Final: true}
	block := func(reasons ...string) gateResult {
		result.Reasons = append(result.Reasons, reasons...)
		result.UserMessage = gateUserMessage(contract, result.Reasons)
		return result
	}
	if len(contract.VerifyCommands) == 0 || options.WorkspaceFingerprint == nil {
		return block("functional acceptance requires test commands and a workspace fingerprint")
	}
	if options.PipelinePrecondition != nil {
		if missing := options.PipelinePrecondition(); len(missing) > 0 {
			result.Final = terminal
			return block(missing...)
		}
	}
	if options.CandidatePatch != nil {
		changed, err := options.CandidatePatch(ctx)
		if err != nil {
			return block("could not verify candidate patch: " + err.Error())
		}
		if !changed {
			return block("no production-code change relative to the task baseline")
		}
	}

	for attempt := 1; attempt <= 2; attempt++ {
		fingerprint, err := options.WorkspaceFingerprint()
		if err != nil {
			return block("workspace fingerprint unavailable: " + err.Error())
		}
		result.Fingerprint = fingerprint
		var failures []string
		var unavailable []string
		evidence := make([]Evidence, 0, len(contract.VerifyCommands))
		for i, verify := range contract.VerifyCommands {
			check := a.executeGateVerify(ctx, step, i, verify)
			check.Fingerprint = fingerprint
			evidence = append(evidence, check)
			if !check.Passed {
				reason := fmt.Sprintf("%s: exit %d; %s", check.Name, check.ExitCode, check.Detail)
				if check.ExitCode < 0 || check.ExitCode >= 124 {
					unavailable = append(unavailable, reason)
				} else {
					failures = append(failures, reason)
				}
			}
		}
		contract.Evidence = evidence
		status := "passed"
		if len(unavailable) > 0 {
			status = "unavailable"
		} else if len(failures) > 0 {
			status = "failed"
		}
		data, _ := json.Marshal(struct {
			Attempt  int        `json:"attempt"`
			Status   string     `json:"status"`
			Evidence []Evidence `json:"evidence"`
		}{attempt, status, evidence})
		a.emit(Event{Kind: FunctionalVerification, Step: step, Data: string(data)})
		if len(unavailable) > 0 {
			return block(append(unavailable, failures...)...)
		}
		if len(failures) == 0 {
			break
		}
		if attempt == 2 {
			return block(failures...)
		}
		a.emit(Event{Kind: FunctionalRepairStarted, Step: step, Data: strings.Join(failures, "\n")})
		prompt := "Repair the current candidate for this task using the visible functional test results below. Keep the existing work, change only what is necessary, and do not modify existing tests. This is the only repair attempt; the framework will rerun the tests.\n\nTask: " +
			contract.Goal + "\n\nFunctional failures:\n" + strings.Join(failures, "\n")
		if hasGateTool(a.runner, "git_diff") {
			diff, err := a.runner.Execute(ctx, ToolCall{ID: "functional-repair-diff", Name: "git_diff", Arguments: json.RawMessage(`{}`)})
			if err == nil {
				prompt += "\n\nCurrent patch:\n" + diff.Observation
			}
		}
		if err := options.FunctionalRepair(ctx, prompt); err != nil {
			a.emit(Event{Kind: FunctionalRepairFinished, Step: step, Data: err.Error()})
			// A worker may have edited the patch before stopping; verify its
			// actual output even when the repair call did not finish cleanly.
		} else {
			a.emit(Event{Kind: FunctionalRepairFinished, Step: step, Data: "returned"})
		}
	}

	if options.CandidatePatch != nil {
		changed, err := options.CandidatePatch(ctx)
		if err != nil {
			return block("could not verify candidate patch: " + err.Error())
		}
		if !changed {
			return block("no production-code change relative to the task baseline")
		}
	}
	if contract.Review {
		result.ReviewAttempted = true
		if _, err := a.executeGateReview(ctx, step, contract, result.Fingerprint, true); err != nil {
			return block("static reviewer did not deliver a valid verdict: " + err.Error())
		}
	}
	result.Passed = true
	return result
}
