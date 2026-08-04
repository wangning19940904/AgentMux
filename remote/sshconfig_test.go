package remote

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSSHHostsResolvesAliasesDefaultsAndIncludes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(sshDir, "config.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `
Host build
  HostName 10.0.0.8
  User deploy
  Port 2222
  IdentityFile ~/.ssh/build_ed25519

Host *.internal
  User wildcard-only

Include config.d/*

Host *
  User fallback-user
  Port 2200
`
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	included := `
Host staging
  HostName staging.internal
  ProxyJump bastion
Host bastion
  HostName bastion.example.com
  User ops
`
	if err := os.WriteFile(filepath.Join(sshDir, "config.d", "team"), []byte(included), 0o600); err != nil {
		t.Fatal(err)
	}

	hosts, err := DiscoverSSHHosts(filepath.Join(sshDir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 3 {
		t.Fatalf("hosts = %+v, want 3 concrete aliases", hosts)
	}
	byName := make(map[string]DiscoveredHost, len(hosts))
	for _, host := range hosts {
		byName[host.Name] = host
	}
	build := byName["build"]
	if build.Host != "10.0.0.8" || build.User != "deploy" || build.Port != 2222 {
		t.Fatalf("build = %+v", build)
	}
	if build.SSHAlias != "build" {
		t.Fatalf("build SSH alias = %q", build.SSHAlias)
	}
	if build.KeyPath != filepath.Join(sshDir, "build_ed25519") {
		t.Fatalf("build key = %q", build.KeyPath)
	}
	staging := byName["staging"]
	if staging.Host != "staging.internal" || staging.User != "fallback-user" ||
		staging.Port != 2200 || staging.ProxyJump != "bastion" {
		t.Fatalf("staging = %+v", staging)
	}
	bastion := byName["bastion"]
	if bastion.Host != "bastion.example.com" || bastion.User != "ops" || bastion.Port != 2200 {
		t.Fatalf("bastion = %+v", bastion)
	}
}

func TestDiscoverSSHHostsUsesFirstObtainedValueAndFiltersPatterns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `
Host *
  Port 2022

Host !prod prod*
  User ignored-pattern

Host dev
  HostName dev.example.com
  Port 22

Host dev
  HostName must-not-win.example.com
`
	path := filepath.Join(sshDir, "config")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	hosts, err := DiscoverSSHHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 {
		t.Fatalf("hosts = %+v, want only dev", hosts)
	}
	if hosts[0].Name != "dev" || hosts[0].Host != "dev.example.com" || hosts[0].Port != 2022 {
		t.Fatalf("dev = %+v", hosts[0])
	}
}

func TestDiscoverSSHHostsMissingConfigIsEmpty(t *testing.T) {
	hosts, err := DiscoverSSHHosts(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 0 {
		t.Fatalf("hosts = %+v, want empty", hosts)
	}
}

func TestSplitSSHConfigFieldsSupportsEqualsQuotesAndComments(t *testing.T) {
	fields := splitSSHConfigFields(`IdentityFile="~/.ssh/key with spaces" # comment`)
	key, values := sshDirectiveParts(fields)
	if key != "identityfile" || len(values) != 1 || values[0] != "~/.ssh/key with spaces" {
		t.Fatalf("key = %q values = %#v", key, values)
	}
}
