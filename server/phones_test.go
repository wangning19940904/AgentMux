package server

import (
	"net/http"
	"testing"
)

func TestParsePhoneAgent(t *testing.T) {
	cases := []struct {
		agent string
		ok    bool
		want  phoneApp
	}{
		{"AgentMux-Android/0.2.0 (Xiaomi 23127PN0CC; Android 15)", true, phoneApp{Platform: "android", Name: "Xiaomi 23127PN0CC", OS: "Android 15", Version: "0.2.0"}},
		{"AgentMux-iOS/0.1.0 (iPhone15,2; iOS 18.1)", true, phoneApp{Platform: "ios", Name: "iPhone15,2", OS: "iOS 18.1", Version: "0.1.0"}},
		{"AgentMux-Android/0.2.0 (%E7%8E%8B%E5%AE%81%E7%9A%84%E6%89%8B%E6%9C%BA; Android 14)", true, phoneApp{Platform: "android", Name: "王宁的手机", OS: "Android 14", Version: "0.2.0"}},
		{"AgentMux-Android/0.2.0", true, phoneApp{Platform: "android", Version: "0.2.0"}},
		{"okhttp/4.12.0", true, phoneApp{Platform: "android"}},
		{"AgentMux/1 CFNetwork/1568.100.1 Darwin/24.0.0", true, phoneApp{Platform: "ios"}},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 Safari/604.1", false, phoneApp{}},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/131.0 Safari/537.36", false, phoneApp{}},
		{"", false, phoneApp{}},
	}
	for _, c := range cases {
		got, ok := parsePhoneAgent(c.agent)
		if ok != c.ok || got != c.want {
			t.Errorf("parsePhoneAgent(%q) = %+v, %v; want %+v, %v", c.agent, got, ok, c.want, c.ok)
		}
	}
}

func TestPhonesListAndSignOut(t *testing.T) {
	host := newAccountHost(t, true)
	bridge := host.bearer(t, "bridge-secret")
	bridge.mustOK(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "root", "password": "root-password-1"}, nil)
	admin := host.browser(t)
	if code := admin.signIn("root", "root-password-1"); code != http.StatusOK {
		t.Fatalf("admin sign-in = %d", code)
	}
	admin.mustOK(http.MethodPost, "/api/v1/accounts", map[string]any{"login": "bob", "password": "bob-password-1"}, nil)

	phone := func(login, agent string) *browser {
		t.Helper()
		b := host.browser(t)
		code := b.doWith(http.MethodPost, "/api/v1/auth/sign-in", map[string]string{"login": login, "password": login + "-password-1"},
			nil, map[string]string{"User-Agent": agent})
		if code != http.StatusOK {
			t.Fatalf("%s sign-in = %d", login, code)
		}
		return b
	}
	bobPhone := phone("bob", "AgentMux-Android/0.2.0 (Pixel 8; Android 15)")
	rootPhone := phone("root", "AgentMux-iOS/0.1.0 (iPhone15,2; iOS 18.1)")

	list := func(b *browser) []phoneView {
		t.Helper()
		var response struct {
			Phones []phoneView `json:"phones"`
		}
		b.mustOK(http.MethodGet, "/api/v1/devices/phones", nil, &response)
		return response.Phones
	}

	// The administrator's browser is no phone; both phones are listed.
	all := list(admin)
	if len(all) != 2 {
		t.Fatalf("admin sees %+v", all)
	}
	byName := map[string]phoneView{}
	for _, p := range all {
		byName[p.Name] = p
		if !p.Active || p.Current {
			t.Fatalf("phone %+v: want active, not current", p)
		}
	}
	if p := byName["Pixel 8"]; p.Platform != "android" || p.AccountName != "bob" || p.OS != "Android 15" || p.AppVersion != "0.2.0" {
		t.Fatalf("bob's phone = %+v", p)
	}
	if p := byName["iPhone15,2"]; p.Platform != "ios" || p.AccountName != "root" {
		t.Fatalf("root's phone = %+v", p)
	}

	// A member sees only their own phone, marked as the one asking.
	mine := list(bobPhone)
	if len(mine) != 1 || mine[0].Name != "Pixel 8" || !mine[0].Current {
		t.Fatalf("bob sees %+v", mine)
	}
	// ...and cannot sign out someone else's.
	if code := bobPhone.do(http.MethodDelete, "/api/v1/devices/phones?id="+byName["iPhone15,2"].ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("bob signing out root's phone = %d", code)
	}

	// The administrator signs bob's phone out; it is no longer signed in.
	admin.mustOK(http.MethodDelete, "/api/v1/devices/phones?id="+byName["Pixel 8"].ID, nil, nil)
	if state := bobPhone.session(); state.Authenticated {
		t.Fatal("bob's phone is still signed in")
	}
	if got := list(admin); len(got) != 1 || got[0].Name != "iPhone15,2" {
		t.Fatalf("after sign-out admin sees %+v", got)
	}
	if !list(rootPhone)[0].Current {
		t.Fatal("root's phone is not marked current")
	}
}
