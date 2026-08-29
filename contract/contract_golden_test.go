package contract

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

var update = flag.Bool("update", false, "rewrite golden schema files")

// contractTypes are the wire types covered by the public contract. Renaming,
// retyping or removing a JSON field here is a breaking change: it must bump
// the contract major version and regenerate goldens with `go test -update`.
var contractTypes = map[string]any{
	"invocation_request.json":      core.InvocationRequest{},
	"invocation_result.json":       core.InvocationResult{},
	"invocation_stream_event.json": core.InvocationStreamEvent{},
	"agent_instance.json":          core.AgentInstance{},
	"channel.json":                 core.Channel{},
	"trigger.json":                 core.Trigger{},
	"orchestration.json":           core.Orchestration{},
	"orchestration_task.json":      core.OrchestrationTask{},
	"turn_usage.json":              core.TurnUsage{},
	"tenant.json":                  core.Tenant{},
}

func TestContractGolden(t *testing.T) {
	for name, value := range contractTypes {
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			schema := canonicalSchema(reflect.TypeOf(value))
			rendered, err := json.MarshalIndent(schema, "", "  ")
			if err != nil {
				t.Fatalf("marshal schema: %v", err)
			}
			rendered = append(rendered, '\n')
			path := filepath.Join("schemas", name)
			if *update {
				if err := os.MkdirAll("schemas", 0o755); err != nil {
					t.Fatalf("create schemas dir: %v", err)
				}
				if err := os.WriteFile(path, rendered, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			golden, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s (run `go test ./contract/ -run TestContractGolden -update` after intentional contract changes): %v", path, err)
			}
			if string(golden) != string(rendered) {
				t.Errorf("contract drift in %s.\nThe Go type no longer matches the published contract.\n"+
					"If the change is intentional: update contract/openapi.yaml, the SDK models, CONTRACT.md,\n"+
					"bump contract.Version when required, then regenerate goldens with\n"+
					"`go test ./contract/ -run TestContractGolden -update`.\n--- golden ---\n%s\n--- current ---\n%s",
					path, golden, rendered)
			}
		})
	}
}

// canonicalSchema reduces a Go struct type to a deterministic JSON-friendly
// field map: wire name -> {type, omitempty}. It intentionally captures only
// what a wire consumer observes.
func canonicalSchema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	fields := map[string]any{}
	collectFields(t, fields)
	return map[string]any{
		"go_type": t.String(),
		"fields":  fields,
	}
}

func collectFields(t reflect.Type, out map[string]any) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if field.Anonymous && name == "" {
			embedded := field.Type
			for embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				collectFields(embedded, out)
				continue
			}
		}
		if name == "" {
			name = field.Name
		}
		out[name] = map[string]any{
			"type":      wireType(field.Type),
			"omitempty": strings.Contains(opts, "omitempty"),
		}
	}
}

var timeType = reflect.TypeOf(time.Time{})

func wireType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return wireType(t.Elem())
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "bytes"
		}
		return "array<" + wireType(t.Elem()) + ">"
	case reflect.Map:
		return fmt.Sprintf("map<%s,%s>", wireType(t.Key()), wireType(t.Elem()))
	case reflect.Struct:
		if t == timeType {
			return "datetime"
		}
		return "object:" + t.String()
	case reflect.Interface:
		return "any"
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	default:
		return t.Kind().String()
	}
}

// TestContractTypeInventory guards against silently dropping a type from the
// golden set.
func TestContractTypeInventory(t *testing.T) {
	want := []string{
		"agent_instance.json", "channel.json", "invocation_request.json",
		"invocation_result.json", "invocation_stream_event.json",
		"orchestration.json", "orchestration_task.json", "tenant.json",
		"trigger.json", "turn_usage.json",
	}
	var got []string
	for name := range contractTypes {
		got = append(got, name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("contract type inventory changed.\nwant: %v\ngot:  %v", want, got)
	}
}
