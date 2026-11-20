package superagent

import (
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestRouterTurnClass(t *testing.T) {
	cases := []struct {
		trigger core.SuperAgentTrigger
		want    core.TurnClass
	}{
		{core.SuperAgentTrigger{SourceKind: core.SuperAgentSourceFeishuMessage}, core.TurnInteractive},
		{core.SuperAgentTrigger{SourceKind: "console"}, core.TurnInteractive},
		{core.SuperAgentTrigger{Kind: core.SuperAgentTriggerPlan}, core.TurnInteractive},
		{core.SuperAgentTrigger{Kind: core.SuperAgentTriggerTicketUpdate}, core.TurnTicket},
		{core.SuperAgentTrigger{SourceKind: core.SuperAgentSourceMailbox}, core.TurnTicket},
		{core.SuperAgentTrigger{Kind: core.SuperAgentTriggerTimer}, core.TurnScheduled},
		{core.SuperAgentTrigger{Kind: core.SuperAgentTriggerSupervision}, core.TurnScheduled},
	}
	for _, tc := range cases {
		if got := routerTurnClass(tc.trigger); got != tc.want {
			t.Fatalf("%+v: class %v, want %v", tc.trigger, got, tc.want)
		}
	}
}
