package claudecode

import (
	"reflect"
	"testing"
)

func TestSessionArgsIncludeModel(t *testing.T) {
	s, err := newSessionResume(&Agent{
		systemPrompt:    "be terse",
		defaultModel:    "sonnet",
		supportedModels: []string{"sonnet", "opus"},
	}, "/tmp/work", "native-1")
	if err != nil {
		t.Fatal(err)
	}
	got := s.args("hello")
	want := []string{
		"--print", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--append-system-prompt", "be terse",
		"--model", "sonnet",
		"--resume", "native-1",
		"hello",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	if err := s.SetModel("opus"); err != nil {
		t.Fatal(err)
	}
	got = s.args("again")
	if got[len(got)-5] != "--model" || got[len(got)-4] != "opus" {
		t.Fatalf("switched args = %#v", got)
	}
}
