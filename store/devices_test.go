package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func newDeviceStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestDevicePairingAndAuthentication(t *testing.T) {
	st := newDeviceStore(t)
	ctx := context.Background()

	code, err := st.CreateDevicePairingCode(ctx, " build box ")
	if err != nil {
		t.Fatal(err)
	}
	if len(code.Code) != pairingCodeLength || code.ExpiresAt.Sub(code.CreatedAt) != DevicePairingCodeTTL {
		t.Fatalf("code = %+v", code)
	}
	if device, _, err := st.RedeemDevicePairingCode(ctx, "WRONG234", core.DeviceInfo{}); device != nil || err != nil {
		t.Fatalf("wrong code paired %+v, %v", device, err)
	}

	// A retyped code works: lower case, spaces and dashes are ignored.
	typed := " " + string([]rune(code.Code)[:4]) + "-" + string([]rune(code.Code)[4:]) + " "
	info := core.DeviceInfo{Name: "ignored", Hostname: "box.local", OS: "linux", Arch: "arm64", Version: "1.2.3"}
	device, token, err := st.RedeemDevicePairingCode(ctx, typed, info)
	if err != nil || device == nil {
		t.Fatalf("redeem: %+v, %v", device, err)
	}
	if device.Name != "build box" || device.Hostname != "box.local" || token == "" || device.TokenPrefix == token {
		t.Fatalf("device = %+v token set=%v", device, token != "")
	}
	if again, _, err := st.RedeemDevicePairingCode(ctx, code.Code, info); again != nil || err != nil {
		t.Fatalf("a used code paired again: %+v, %v", again, err)
	}

	found, err := st.AuthenticateDevice(ctx, token)
	if err != nil || found == nil || found.ID != device.ID {
		t.Fatalf("authenticate: %+v, %v", found, err)
	}
	for _, bad := range []string{"", "amxd_nope", "amxt_" + token} {
		if found, err := st.AuthenticateDevice(ctx, bad); found != nil || err != nil {
			t.Fatalf("token %q authenticated: %+v, %v", bad, found, err)
		}
	}
	var stored string
	if err := st.db.QueryRowContext(ctx, `SELECT token_hash FROM devices WHERE id=?`, device.ID).Scan(&stored); err != nil || stored == token || stored != HashSecret(token) {
		t.Fatalf("the token must be stored hashed: %q, %v", stored, err)
	}

	seen := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	if err := st.TouchDevice(ctx, device.ID, core.DeviceInfo{Hostname: "box.local", OS: "linux", Arch: "arm64", Version: "1.2.4"}, seen); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchDevice(ctx, device.ID, core.DeviceInfo{}, seen.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.RenameDevice(ctx, device.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	devices, err := st.ListDevices(ctx)
	if err != nil || len(devices) != 1 {
		t.Fatalf("list: %+v, %v", devices, err)
	}
	got := devices[0]
	if got.Name != "renamed" || got.Version != "1.2.4" || got.LastSeenAt == nil || !got.LastSeenAt.Equal(seen.Add(time.Minute)) {
		t.Fatalf("device after touch = %+v", got)
	}

	if err := st.DeleteDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if found, err := st.AuthenticateDevice(ctx, token); found != nil || err != nil {
		t.Fatalf("a deleted device authenticated: %+v, %v", found, err)
	}
	if found, err := st.GetDevice(ctx, device.ID); found != nil || err != nil {
		t.Fatalf("a deleted device is still there: %+v, %v", found, err)
	}
}

func TestDevicePairingCodeExpiresAndIsSingleUseUnderRace(t *testing.T) {
	st := newDeviceStore(t)
	ctx := context.Background()

	expired, err := st.CreateDevicePairingCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.writer.ExecContext(ctx, `UPDATE device_pairing_codes SET expires_at=? WHERE id=?`,
		time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano), expired.ID); err != nil {
		t.Fatal(err)
	}
	if device, _, err := st.RedeemDevicePairingCode(ctx, expired.Code, core.DeviceInfo{}); device != nil || err != nil {
		t.Fatalf("an expired code paired: %+v, %v", device, err)
	}

	code, err := st.CreateDevicePairingCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	paired := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			device, _, err := st.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo{Hostname: "racer"})
			if err == nil && device != nil {
				mu.Lock()
				paired++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if paired != 1 {
		t.Fatalf("one code paired %d devices", paired)
	}
	if devices, _ := st.ListDevices(ctx); len(devices) != 1 || devices[0].Name != "racer" {
		t.Fatalf("devices = %+v", devices)
	}
}
