package framework

import (
	"context"
	"strings"
	"testing"
)

func TestCatalogHasKnownFrameworks(t *testing.T) {
	want := map[string]KindType{
		"claudecode":       KindCLI,
		"claude-agent-sdk": KindSDK,
		"openai-agents":    KindSDK,
		"deepagents":       KindSDK,
	}
	for kind, kt := range want {
		spec, ok := Lookup(kind)
		if !ok {
			t.Fatalf("catalog missing %q", kind)
		}
		if spec.KindType != kt {
			t.Fatalf("%q kind_type = %q, want %q", kind, spec.KindType, kt)
		}
	}
}

func TestLookupUnknownReturnsFalse(t *testing.T) {
	if _, ok := Lookup("does-not-exist"); ok {
		t.Fatal("expected unknown framework lookup to fail")
	}
}

func TestDetectCLIUsesLookPath(t *testing.T) {
	// A CLI whose binary is almost certainly not present must report as not
	// installed rather than erroring.
	spec := Spec{Kind: "fake-cli", KindType: KindCLI, Bin: "definitely-not-a-real-binary-xyz"}
	st := Detect(spec, DetectPrereqs())
	if st.Installed {
		t.Fatal("expected fake CLI to be not installed")
	}
}

func TestInstallRejectsUnknown(t *testing.T) {
	res := Install(context.Background(), "not-a-framework")
	if res.OK || !strings.Contains(res.Error, "unknown framework") {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestInstallRejectsCLIFramework(t *testing.T) {
	res := Install(context.Background(), "claudecode")
	if res.OK || !strings.Contains(res.Error, "CLI") {
		t.Fatalf("expected CLI rejection, got: %+v", res)
	}
}

func TestInstallRejectsUnsupportedFramework(t *testing.T) {
	// deepagents is catalogued but not supported for automatic install.
	res := Install(context.Background(), "deepagents")
	if res.OK {
		t.Fatalf("expected deepagents install to be refused, got: %+v", res)
	}
	if !strings.Contains(res.Error, "not yet supported") && !strings.Contains(res.Error, "runtime") {
		t.Fatalf("unexpected deepagents error: %q", res.Error)
	}
}
