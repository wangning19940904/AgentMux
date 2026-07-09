package cliagents

import (
	"reflect"
	"testing"
)

func TestModelArgsForVerifiedCLIs(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{
			name: "codex",
			got:  codexArgs("hello", "", "gpt-5"),
			want: []string{"exec", "--json", "--model", "gpt-5", "hello"},
		},
		{
			name: "cursor",
			got:  cursorArgs("hello", "", "sonnet-4"),
			want: []string{"agent", "--print", "--output-format", "stream-json", "--model", "sonnet-4", "hello"},
		},
	}
	for _, tt := range tests {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Fatalf("%s args = %#v, want %#v", tt.name, tt.got, tt.want)
		}
	}
}
