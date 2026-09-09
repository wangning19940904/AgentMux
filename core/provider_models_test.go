package core

import (
	"reflect"
	"testing"
)

func TestProviderModelOptionsBlockAndRestore(t *testing.T) {
	p := &Provider{Model: "bad", Meta: ProviderMeta{SupportedModels: []string{"bad", "ready", "ready"}, BlockedModels: []string{"bad"}}}
	if got := ProviderModelOptions(p); !reflect.DeepEqual(got, []string{"ready"}) {
		t.Fatalf("options = %v", got)
	}
	p.Meta.BlockedModels = nil
	if got := ProviderModelOptions(p); !reflect.DeepEqual(got, []string{"bad", "ready"}) {
		t.Fatalf("restored options = %v", got)
	}
	p.Meta.BlockedModels = []string{"bad", "ready"}
	if got := ProviderModelOptions(p); len(got) != 0 {
		t.Fatalf("all blocked options = %v", got)
	}
	if len(p.Meta.SupportedModels) != 3 {
		t.Fatal("filter mutated discovery catalog")
	}
}
