package superagent

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// The brain says what "done" means when it accepts work: up to a few short
// acceptance criteria, and how long it expects a turn to take. The worker is
// asked for evidence per criterion, the brain sees them with the Ticket, and
// supervision uses the expected time to tell slow work from stalled work.

const (
	maxDoneWhen         = 6
	maxCriterionRunes   = 300
	maxExpectMinutes    = int(workerTimeout / time.Minute)
	criteriaEvidenceAsk = "DONE WHEN (end your report with one line per criterion: whether it is met and the evidence, such as a command and its result, a commit, or a file and line; say plainly when one is not met):"
)

// checkCriteria refuses malformed acceptance criteria and expected times. It
// is part of preflight, so a bad value starts nothing and the router is told
// why.
func checkCriteria(doneWhen []string, expectMinutes int) error {
	if len(doneWhen) > maxDoneWhen {
		return fmt.Errorf("done_when has %d criteria; give at most %d short ones", len(doneWhen), maxDoneWhen)
	}
	seen := map[string]bool{}
	for index, criterion := range doneWhen {
		criterion = strings.TrimSpace(criterion)
		if criterion == "" {
			return fmt.Errorf("done_when criterion %d is empty", index+1)
		}
		if utf8.RuneCountInString(criterion) > maxCriterionRunes {
			return fmt.Errorf("done_when criterion %d is longer than %d characters; keep each one short and checkable", index+1, maxCriterionRunes)
		}
		if strings.ContainsAny(criterion, "\r\n") {
			return fmt.Errorf("done_when criterion %d spans several lines; give one line each", index+1)
		}
		if seen[strings.ToLower(criterion)] {
			return fmt.Errorf("done_when criterion %d repeats another", index+1)
		}
		seen[strings.ToLower(criterion)] = true
	}
	if expectMinutes < 0 || expectMinutes > maxExpectMinutes {
		return fmt.Errorf("expect_minutes must be between 1 and %d (the longest a turn may run)", maxExpectMinutes)
	}
	return nil
}

// cleanCriteria trims checked criteria. Nil stays nil, so a Ticket without
// criteria is stored as it was before they existed.
func cleanCriteria(doneWhen []string) []string {
	var out []string
	for _, criterion := range doneWhen {
		out = append(out, strings.TrimSpace(criterion))
	}
	return out
}

// writeCriteria appends the acceptance criteria to a worker's instructions,
// with the request for evidence.
func writeCriteria(prompt *strings.Builder, doneWhen []string, expectMinutes int) {
	if len(doneWhen) > 0 {
		prompt.WriteString("\n" + criteriaEvidenceAsk + "\n")
		for index, criterion := range doneWhen {
			fmt.Fprintf(prompt, "%d. %s\n", index+1, criterion)
		}
	}
	if expectMinutes > 0 {
		fmt.Fprintf(prompt, "\nThe router expects this to take about %d minutes. If it will take much longer, say why in your report.\n", expectMinutes)
	}
}
