package framework

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeInstallSettings(t *testing.T) {
	got, err := NormalizeInstallSettings(InstallSettings{
		DefaultRegistry: " https://bnpm.byted.org ",
		Frameworks: map[string]FrameworkOverride{
			"gemini": {Registry: "https://registry.npmmirror.com", InstallCommand: "  npm i -g @google/gemini-cli@nightly  "},
			"codex":  {},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultRegistry != "https://bnpm.byted.org/" {
		t.Fatalf("default registry = %q", got.DefaultRegistry)
	}
	if _, kept := got.Frameworks["codex"]; kept || len(got.Frameworks) != 1 {
		t.Fatalf("empty overrides should be dropped: %+v", got.Frameworks)
	}
	if gemini := got.Frameworks["gemini"]; gemini.Registry != "https://registry.npmmirror.com/" || gemini.InstallCommand != "npm i -g @google/gemini-cli@nightly" {
		t.Fatalf("gemini override = %+v", gemini)
	}

	for name, bad := range map[string]InstallSettings{
		"scheme":       {DefaultRegistry: "file:///etc/passwd"},
		"credentials":  {DefaultRegistry: "https://user:secret@registry.example.com/"},
		"flag smuggle": {DefaultRegistry: "https://registry.example.com/ --ignore-scripts"},
		"unknown kind": {Frameworks: map[string]FrameworkOverride{"nope": {Registry: "https://registry.example.com/"}}},
		"multi-line":   {Frameworks: map[string]FrameworkOverride{"gemini": {UpdateCommand: "npm i -g x\nrm -rf ~"}}},
		"too long":     {Frameworks: map[string]FrameworkOverride{"gemini": {UpdateCommand: strings.Repeat("x", maxCustomCommandLength+1)}}},
	} {
		if _, err := NormalizeInstallSettings(bad); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestPlanCommandAppliesRegistries(t *testing.T) {
	gemini, _ := Lookup("gemini")
	dsh, _ := Lookup("dsh")
	claude, _ := Lookup("claudecode")
	def := InstallSettings{DefaultRegistry: "https://bnpm.byted.org/"}

	for _, test := range []struct {
		name     string
		spec     Spec
		action   string
		settings InstallSettings
		want     string
	}{
		{"built-in without settings", gemini, "install", InstallSettings{}, "npm install -g @google/gemini-cli@latest"},
		{"machine default", gemini, "update", def, "npm install -g @google/gemini-cli@latest --registry=https://bnpm.byted.org/"},
		{"catalog scope pin survives the default", dsh, "install", def,
			"npm install -g @deepseek-ai/dsh@latest --@deepseek-ai:registry=https://registry.npmjs.org/ --replace-registry-host=never --registry=https://bnpm.byted.org/"},
		{"framework registry replaces the catalog pin", dsh, "install", InstallSettings{Frameworks: map[string]FrameworkOverride{"dsh": {Registry: "https://registry.npmmirror.com/"}}},
			"npm install -g @deepseek-ai/dsh@latest --registry=https://registry.npmmirror.com/"},
		{"native updater ignores registries", claude, "update", def, "claude update"},
		{"npm install of a native-update CLI", claude, "install", def, "npm install -g @anthropic-ai/claude-code@latest --registry=https://bnpm.byted.org/"},
	} {
		plan, err := PlanCommand(test.spec, test.action, test.settings)
		if err != nil || plan.Custom || plan.Command != test.want || strings.Join(plan.Argv, " ") != test.want {
			t.Errorf("%s: plan = %+v, err = %v; want %q", test.name, plan, err, test.want)
		}
	}

	custom := InstallSettings{
		DefaultRegistry: "https://bnpm.byted.org/",
		Frameworks:      map[string]FrameworkOverride{"gemini": {UpdateCommand: "pnpm add -g @google/gemini-cli"}},
	}
	plan, err := PlanCommand(gemini, "update", custom)
	if err != nil || !plan.Custom || plan.Command != "pnpm add -g @google/gemini-cli" || plan.Registry != "" {
		t.Fatalf("custom plan = %+v, err = %v", plan, err)
	}
	if runtime.GOOS != "windows" && strings.Join(plan.Argv, "|") != "/bin/sh|-c|pnpm add -g @google/gemini-cli" {
		t.Fatalf("custom argv = %q", plan.Argv)
	}
	// The install command is still built in, with the machine default.
	if plan, _ := PlanCommand(gemini, "install", custom); plan.Custom || !strings.HasSuffix(plan.Command, "--registry=https://bnpm.byted.org/") {
		t.Fatalf("install plan with only a custom update = %+v", plan)
	}
}

func TestCustomInstallCommandNeedsConfirmationAndRunsLocally(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	// The custom command writes a fake gemini binary, proving the shell ran it.
	command := "printf '#!/bin/sh\\necho gemini 9.9.9\\n' > '" + filepath.Join(bin, "gemini") + "' && chmod +x '" + filepath.Join(bin, "gemini") + "'"
	settings := InstallSettings{Frameworks: map[string]FrameworkOverride{"gemini": {InstallCommand: command}}}

	res := InstallWithProgressOptions(context.Background(), "gemini", InstallOptions{Settings: settings}, nil)
	if res.OK || !strings.Contains(res.Error, ErrCustomCommandUnconfirmed.Error()) || res.Command != command {
		t.Fatalf("unconfirmed custom install = %+v", res)
	}
	res = InstallWithProgressOptions(context.Background(), "gemini", InstallOptions{Settings: settings, ConfirmedCommand: "npm install -g something-else"}, nil)
	if res.OK || !strings.Contains(res.Error, ErrCustomCommandUnconfirmed.Error()) {
		t.Fatalf("custom install confirmed with different text = %+v", res)
	}
	res = InstallWithProgressOptions(context.Background(), "gemini", InstallOptions{Settings: settings, ConfirmedCommand: command, Forwarded: true}, nil)
	if res.OK || !strings.Contains(res.Error, "this machine's own Console") {
		t.Fatalf("forwarded custom install = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(bin, "gemini")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected custom command must not run (stat err = %v)", err)
	}

	res = InstallWithProgressOptions(context.Background(), "gemini", InstallOptions{Settings: settings, ConfirmedCommand: command}, nil)
	if !res.OK || res.Version != "9.9.9" || res.Command != command {
		t.Fatalf("confirmed custom install = %+v", res)
	}
}

func TestLatestVersionReadsTheFrameworkRegistry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	mockFrameworkLatestVersion(t, "dsh", "0.2.1-rc.5")
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "dsh"), "#!/bin/sh\necho 0.2.1-rc.4\n")
	// npm prints the registry it was asked to read so the test can see it.
	writeFrameworkExecutable(t, filepath.Join(bin, "npm"), "#!/bin/sh\n"+
		"case \" $* \" in *' --registry=https://registry.npmmirror.com/ '*) echo 0.2.1-rc.9 ;; *) echo 0.0.1 ;; esac\n")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NVM_DIR", "")
	t.Setenv("PNPM_HOME", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")

	// Without a framework registry the catalog's official endpoint decides,
	// even when the machine has a default registry.
	check := CheckUpdateWithSettings(context.Background(), "dsh", InstallSettings{DefaultRegistry: "https://bnpm.byted.org/"})
	if check.Error != "" || check.LatestVersion != "0.2.1-rc.5" {
		t.Fatalf("dsh check with default registry = %+v", check)
	}
	check = CheckUpdateWithSettings(context.Background(), "dsh", InstallSettings{
		Frameworks: map[string]FrameworkOverride{"dsh": {Registry: "https://registry.npmmirror.com/"}},
	})
	if check.Error != "" || check.LatestVersion != "0.2.1-rc.9" || !check.UpdateAvailable {
		t.Fatalf("dsh check with framework registry = %+v", check)
	}
}
