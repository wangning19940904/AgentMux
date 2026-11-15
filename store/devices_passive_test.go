package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

const passiveTestUID = "6f1c1d0e-3c7b-4e8e-9a51-2b7f0c9e4d21"

func TestPassiveDeviceSaveReplaceAndDelete(t *testing.T) {
	st := newDeviceStore(t)
	ctx := context.Background()
	info := core.DeviceInfo{Name: "box", Hostname: "box.local", OS: "linux", Arch: "amd64", Version: "1", MachineUID: passiveTestUID}
	link := core.PassiveLink{Addrs: []string{"192.0.2.1:8767"}, Fingerprint: strings.Repeat("a", 64), Credential: "amxc_one"}

	device, replaced, err := st.SavePassiveDevice(ctx, "", "", info, link)
	if err != nil || replaced {
		t.Fatalf("save: %+v %v %v", device, replaced, err)
	}
	if device.Name != "box" || device.MachineUID != passiveTestUID {
		t.Fatalf("device = %+v", device)
	}
	links, err := st.PassiveLinks(ctx)
	if err != nil || len(links) != 1 || links[0].DeviceID != device.ID || links[0].Credential != "amxc_one" || links[0].Addrs[0] != "192.0.2.1:8767" {
		t.Fatalf("links = %+v, %v", links, err)
	}
	if id, _ := st.PassiveDeviceForMachine(ctx, passiveTestUID); id != device.ID {
		t.Fatalf("passive device for machine = %q", id)
	}
	// A passive device has no token to dial in with.
	if found, _ := st.AuthenticateDevice(ctx, "amxc_one"); found != nil {
		t.Fatal("the hub's credential authenticates as a device token")
	}

	// The same machine added again: the link is replaced, the id kept.
	link.Credential, link.Addrs = "amxc_two", []string{"192.0.2.2:8767"}
	info.Version = "2"
	again, replaced, err := st.SavePassiveDevice(ctx, "", "", info, link)
	if err != nil || !replaced || again.ID != device.ID || again.Version != "2" {
		t.Fatalf("replace: %+v %v %v", again, replaced, err)
	}
	if got, _ := st.PassiveLink(ctx, device.ID); got == nil || got.Credential != "amxc_two" || got.Addrs[0] != "192.0.2.2:8767" {
		t.Fatalf("link after replace = %+v", got)
	}

	if err := st.DeleteDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if links, _ := st.PassiveLinks(ctx); len(links) != 0 {
		t.Fatalf("links after delete = %+v", links)
	}
}

func TestPassiveDeviceRefusedForAMachineThatDialsIn(t *testing.T) {
	st := newDeviceStore(t)
	ctx := context.Background()
	code, err := st.CreateDevicePairingCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	info := core.DeviceInfo{Name: "box", MachineUID: passiveTestUID}
	if device, _, err := st.RedeemDevicePairingCode(ctx, code.Code, info); err != nil || device == nil {
		t.Fatalf("redeem: %v", err)
	}
	_, _, err = st.SavePassiveDevice(ctx, "", "", info, core.PassiveLink{Addrs: []string{"192.0.2.1:1"}, Fingerprint: "f", Credential: "c"})
	if !errors.Is(err, ErrMachinePairedOutbound) {
		t.Fatalf("save for a machine that dials in: %v", err)
	}
	if links, _ := st.PassiveLinks(ctx); len(links) != 0 {
		t.Fatalf("links = %+v", links)
	}
	// Machines with no UID are never matched with one another.
	if _, _, err := st.SavePassiveDevice(ctx, "", "", core.DeviceInfo{Name: "old"}, core.PassiveLink{Addrs: []string{"192.0.2.1:1"}, Fingerprint: "f", Credential: "c"}); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresPassiveLinksReachTheStandby checks that what a hub needs to dial
// a listening device travels to a standby with the rest of hub data, in a
// snapshot and as changes, and that a fenced primary cannot change it.
func TestPostgresPassiveLinksReachTheStandby(t *testing.T) {
	ctx := context.Background()
	primary := openPostgresIntegrationStore(t)
	standby := openPostgresIntegrationStore(t)
	if err := primary.SetHubRole(ctx, HubRolePrimary, 1); err != nil {
		t.Fatal(err)
	}
	info := core.DeviceInfo{Name: "box", MachineUID: passiveTestUID}
	link := core.PassiveLink{Addrs: []string{"192.0.2.1:8767"}, Fingerprint: strings.Repeat("a", 64), Credential: "amxc_one"}
	device, _, err := primary.SavePassiveDevice(ctx, "", "", info, link)
	if err != nil {
		t.Fatal(err)
	}
	if err := standby.SetHubRole(ctx, HubRoleStandby, 0); err != nil {
		t.Fatal(err)
	}
	snapshotHub(t, primary, standby)
	if got, err := standby.PassiveLink(ctx, device.ID); err != nil || got == nil || got.Credential != "amxc_one" {
		t.Fatalf("standby link after snapshot = %+v, %v", got, err)
	}

	link.Credential, link.Addrs = "amxc_two", []string{"192.0.2.2:8767", "192.0.2.3:8767"}
	if _, replaced, err := primary.SavePassiveDevice(ctx, "", "", info, link); err != nil || !replaced {
		t.Fatalf("replace: %v %v", replaced, err)
	}
	syncHub(t, primary, standby)
	got, err := standby.PassiveLink(ctx, device.ID)
	if err != nil || got == nil || got.Credential != "amxc_two" || len(got.Addrs) != 2 {
		t.Fatalf("standby link after changes = %+v, %v", got, err)
	}
	if id, _ := standby.PassiveDeviceForMachine(ctx, passiveTestUID); id != device.ID {
		t.Fatalf("standby passive device for machine = %q", id)
	}
	// A standby cannot add one of its own.
	if _, _, err := standby.SavePassiveDevice(ctx, "", "", core.DeviceInfo{Name: "rogue"}, link); err == nil {
		t.Fatal("a standby saved a passive device")
	}
	// Nor can a fenced primary.
	if err := primary.SetHubRole(ctx, HubRoleFenced, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := primary.SavePassiveDevice(ctx, "", "", info, link); err == nil {
		t.Fatal("a fenced primary changed a passive link")
	}
	if err := primary.SetHubRole(ctx, HubRolePrimary, 1); err != nil {
		t.Fatal(err)
	}
	if err := primary.DeleteDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	syncHub(t, primary, standby)
	if links, _ := standby.PassiveLinks(ctx); len(links) != 0 {
		t.Fatalf("standby links after delete = %+v", links)
	}
}
