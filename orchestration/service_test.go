package orchestration

import (
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestNormalizeRequiresAgentTargetsAndAcyclicDependencies(t *testing.T) {
	if _, err := Normalize("demo", 2, []core.OrchestrationTask{{ID: "a", Input: "missing target"}}); err == nil {
		t.Fatal("expected agent target validation")
	}
	if _, err := Normalize("demo", 2, []core.OrchestrationTask{
		{ID: "a", AgentID: "agent-a", Input: "a", DependsOn: []string{"b"}},
		{ID: "b", AgentID: "agent-b", Input: "b", DependsOn: []string{"a"}},
	}); err == nil {
		t.Fatal("expected cycle validation")
	}
	value, err := Normalize("demo", 2, []core.OrchestrationTask{
		{ID: "a", AgentID: "agent-a", Input: "a"},
		{ID: "b", AgentID: "agent-b", Input: "b", DependsOn: []string{"a", "a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Tasks[1].DependsOn) != 1 || value.Tasks[1].DependsOn[0] != "a" {
		t.Fatalf("dependencies = %+v", value.Tasks[1].DependsOn)
	}
}
