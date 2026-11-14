package framework

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// smokeFake is a registered agent whose session replays scripted events.
type smokeFake struct {
	mu      sync.Mutex
	cfg     map[string]any
	workDir string
	prompt  string
	denied  int
	events  []*core.Event
	hang    bool
}

func (f *smokeFake) Name() string { return "smoke-fake" }
func (f *smokeFake) StartSession(_ context.Context, dir string) (core.AgentSession, error) {
	f.mu.Lock()
	f.workDir = dir
	f.mu.Unlock()
	return &smokeFakeSession{f: f}, nil
}
func (f *smokeFake) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (f *smokeFake) Stop(context.Context) error                     { return nil }

type smokeFakeSession struct{ f *smokeFake }

func (s *smokeFakeSession) ID() string { return "fake" }
func (s *smokeFakeSession) Send(ctx context.Context, text string) (<-chan *core.Event, error) {
	s.f.mu.Lock()
	s.f.prompt = text
	s.f.mu.Unlock()
	out := make(chan *core.Event)
	go func() {
		defer close(out)
		for _, ev := range s.f.events {
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
		if s.f.hang {
			<-ctx.Done()
		}
	}()
	return out, nil
}
func (s *smokeFakeSession) RespondPermission(_ context.Context, allow bool) error {
	if !allow {
		s.f.mu.Lock()
		s.f.denied++
		s.f.mu.Unlock()
	}
	return nil
}
func (s *smokeFakeSession) Close(context.Context) error { return nil }

var (
	smokeFakesMu sync.Mutex
	smokeFakes   = map[string]*smokeFake{}
)

// registerSmokeFake registers (once per name) an agent backed by fake.
func registerSmokeFake(t *testing.T, name string, fake *smokeFake) {
	t.Helper()
	smokeFakesMu.Lock()
	defer smokeFakesMu.Unlock()
	if _, ok := smokeFakes[name]; !ok {
		core.RegisterAgent(name, func(cfg map[string]any) (core.Agent, error) {
			smokeFakesMu.Lock()
			f := smokeFakes[name]
			smokeFakesMu.Unlock()
			f.mu.Lock()
			f.cfg = cfg
			f.mu.Unlock()
			return f, nil
		})
	}
	smokeFakes[name] = fake
}

func TestSmokeTestPassesThroughModelAndRunsSafely(t *testing.T) {
	fake := &smokeFake{events: []*core.Event{
		{Type: core.EventPermission},
		{Type: core.EventOutput, Text: "O"},
		{Type: core.EventOutput, Text: "K", Usage: &core.TurnUsage{ResolvedModel: "deepseek/deepseek-v4-pro"}},
		{Type: core.EventFinal, Text: "OK."},
	}}
	registerSmokeFake(t, "smoke-fake-ok", fake)
	result, err := SmokeTest(context.Background(), SmokeOptions{
		Kind: "smoke-fake-ok", Model: "group/opus-anywhere", ReasoningEffort: "high",
		Env: map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.ErrorKind != "" || result.Reply != "OK." || result.ResolvedModel != "deepseek/deepseek-v4-pro" {
		t.Fatalf("result = %+v", result)
	}
	if fake.prompt != SmokePrompt {
		t.Fatalf("prompt = %q", fake.prompt)
	}
	if fake.cfg["model"] != "group/opus-anywhere" || fake.cfg["reasoning_effort"] != "high" ||
		fake.cfg[core.AgentConfigHealthCheck] != true || fake.cfg["single_agent"] != true {
		t.Fatalf("cfg = %+v", fake.cfg)
	}
	if env, _ := fake.cfg["env"].(map[string]string); env["FOO"] != "bar" {
		t.Fatalf("env = %+v", fake.cfg["env"])
	}
	if fake.denied != 1 {
		t.Fatalf("permission requests must be declined, denied = %d", fake.denied)
	}
	if !strings.Contains(fake.workDir, "agentmux-health-") {
		t.Fatalf("work dir = %q", fake.workDir)
	}
	if _, err := os.Stat(fake.workDir); !os.IsNotExist(err) {
		t.Fatalf("temporary work dir was not removed: %v", err)
	}
}

func TestSmokeTestClassifiesFailures(t *testing.T) {
	cases := []struct {
		name   string
		events []*core.Event
		want   SmokeErrorKind
	}{
		{"router 404", []*core.Event{{Type: core.EventError, Err: errors.New(`unexpected status 404 Not Found: magpie knows no model "x"`)}}, SmokeModelNotFound},
		{"auth", []*core.Event{{Type: core.EventError, Err: errors.New("Invalid API key · Please run /login")}}, SmokeUnauthenticated},
		{"rate", []*core.Event{{Type: core.EventError, Err: errors.New("API Error: 429 rate_limit_error")}}, SmokeRateLimited},
		{"network", []*core.Event{{Type: core.EventError, Err: errors.New("dial tcp 127.0.0.1:3425: connect: connection refused")}}, SmokeNetwork},
		{"api error as reply", []*core.Event{{Type: core.EventFinal, Text: "API Error: 401 authentication_error"}}, SmokeUnauthenticated},
		{"chatty reply", []*core.Event{{Type: core.EventFinal, Text: "Sure! I'd be happy to help you with that request today."}}, SmokeUnexpectedReply},
		{"nothing", nil, SmokeUnknown},
		{"exit only", []*core.Event{{Type: core.EventError, Err: errors.New("exit status 1")}}, SmokeUnknown},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "smoke-fake-fail-" + string(rune('a'+i))
			registerSmokeFake(t, name, &smokeFake{events: tc.events})
			result, err := SmokeTest(context.Background(), SmokeOptions{Kind: name})
			if err != nil {
				t.Fatal(err)
			}
			if result.OK || result.ErrorKind != tc.want || result.ErrorDetail == "" {
				t.Fatalf("result = %+v, want %s", result, tc.want)
			}
		})
	}
}

func TestSmokeTestTimesOut(t *testing.T) {
	registerSmokeFake(t, "smoke-fake-hang", &smokeFake{hang: true})
	result, err := SmokeTest(context.Background(), SmokeOptions{Kind: "smoke-fake-hang", Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.ErrorKind != SmokeTimeout {
		t.Fatalf("result = %+v", result)
	}
}

func TestSmokeTestRejectsUnknownFramework(t *testing.T) {
	if _, err := SmokeTest(context.Background(), SmokeOptions{Kind: "no-such-framework"}); !errors.Is(err, ErrUnknownFramework) {
		t.Fatalf("err = %v", err)
	}
}

func TestSmokeReplyOK(t *testing.T) {
	for reply, want := range map[string]bool{
		"ok": true, "OK": true, "ok.": true, "`ok`": true, "**OK**": true, "Ok!": true, "\"ok\"": true,
		"": false, "token": false, "okay": false, "no": false,
		"ok, but this is a much longer answer than anyone asked for": false,
	} {
		if got := smokeReplyOK(reply); got != want {
			t.Errorf("smokeReplyOK(%q) = %v, want %v", reply, got, want)
		}
	}
}

func TestRedactSecrets(t *testing.T) {
	in := `401: key sk-ant-api03-abcdefghijkl rejected; Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc; {"api_key":"live-secret-123456"}`
	out := redactSecrets(in)
	for _, secret := range []string{"sk-ant-api03-abcdefghijkl", "eyJhbGciOiJIUzI1NiJ9", "live-secret-123456"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked: %s", secret, out)
		}
	}
	if !strings.Contains(out, "401") {
		t.Fatalf("redaction removed useful context: %s", out)
	}
}
