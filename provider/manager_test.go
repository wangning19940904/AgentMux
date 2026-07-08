package provider

import "testing"

func TestNormalizeProtoAliases(t *testing.T) {
	cases := map[string]string{
		"anthropic":        protoAnthropic,
		"claude":           protoAnthropic,
		"openai_chat":      protoOpenAIChat,
		"chat":             protoOpenAIChat,
		"openai_responses": protoResponses,
		"responses":        protoResponses,
		"gemini":           protoGemini,
		"gemini_native":    protoGemini,
		"":                 "",
		"unknown":          "",
	}
	for in, want := range cases {
		if got := normalizeProto(in); got != want {
			t.Errorf("normalizeProto(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpstreamProtoFallback(t *testing.T) {
	if got := upstreamProto("", protoAnthropic); got != protoAnthropic {
		t.Errorf("empty format should fall back to client proto, got %q", got)
	}
	if got := upstreamProto("openai_chat", protoAnthropic); got != protoOpenAIChat {
		t.Errorf("declared format should win, got %q", got)
	}
}
