package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") || strings.Contains(hash, "correct horse") {
		t.Fatalf("hash = %q", hash)
	}
	if !VerifyPassword(hash, "correct horse battery") {
		t.Fatal("the right password was rejected")
	}
	if VerifyPassword(hash, "correct horse battery!") {
		t.Fatal("a wrong password was accepted")
	}
	other, _ := HashPassword("correct horse battery")
	if other == hash {
		t.Fatal("two hashes of one password share a salt")
	}
	for _, malformed := range []string{"", "plain", "$argon2id$v=19$m=999999999,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5", "$bcrypt$x"} {
		if VerifyPassword(malformed, "anything") {
			t.Fatalf("malformed hash %q matched", malformed)
		}
	}
}

func TestFirstAdminAndAccountNamespace(t *testing.T) {
	st := newTenancyStore(t)
	ctx := context.Background()

	if count, err := st.CountAccounts(ctx); err != nil || count != 0 {
		t.Fatalf("fresh store has %d accounts, %v", count, err)
	}
	admin, err := st.CreateFirstAdmin(ctx, NewAccount{Login: " Alice ", DisplayName: "Alice", Role: "member", Password: "alice-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	if admin.Login != "alice" || admin.Role != core.AccountRoleAdmin || !admin.HasPassword || admin.TenantID == "" {
		t.Fatalf("first admin = %+v", admin)
	}
	if _, err := st.CreateFirstAdmin(ctx, NewAccount{Login: "mallory", Password: "mallory-password"}); !errors.Is(err, ErrAccountsExist) {
		t.Fatalf("second first-admin err = %v", err)
	}

	// The account owns a tenant namespace named after its login.
	tenant, err := st.GetTenant(ctx, admin.TenantID)
	if err != nil || tenant == nil || tenant.Name != "alice" || tenant.Kind != core.TenantKindAccount || !tenant.Active() {
		t.Fatalf("namespace = %+v, %v", tenant, err)
	}
	owner, err := st.AccountForTenant(ctx, tenant.ID)
	if err != nil || owner == nil || owner.ID != admin.ID {
		t.Fatalf("account for tenant = %+v, %v", owner, err)
	}

	// Logins are unique across accounts and tenant names.
	if _, err := st.CreateAccount(ctx, NewAccount{Login: "ALICE", Password: "another-password"}); !errors.Is(err, ErrLoginTaken) {
		t.Fatalf("duplicate login err = %v", err)
	}
	now := time.Now().UTC()
	if err := st.UpsertTenant(ctx, &core.Tenant{ID: "ten_app", Name: "webapp", Kind: core.TenantKindWeb, Status: core.TenantStatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(ctx, NewAccount{Login: "webapp", Password: "another-password"}); !errors.Is(err, ErrLoginTaken) {
		t.Fatalf("login colliding with a tenant err = %v", err)
	}
	for _, bad := range []NewAccount{{Login: "x", Password: "long-enough-pw"}, {Login: "has space", Password: "long-enough-pw"}, {Login: "bob", Password: "short"}} {
		if _, err := st.CreateAccount(ctx, bad); err == nil {
			t.Fatalf("invalid account %+v was created", bad)
		}
	}
}

func TestAccountPasswordsAndSessions(t *testing.T) {
	st := newTenancyStore(t)
	ctx := context.Background()
	if _, err := st.CreateFirstAdmin(ctx, NewAccount{Login: "root", Password: "root-password-1"}); err != nil {
		t.Fatal(err)
	}
	member, err := st.CreateAccount(ctx, NewAccount{Login: "bob", DisplayName: "Bob", Password: "bob-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	if member.Role != core.AccountRoleMember {
		t.Fatalf("role = %q", member.Role)
	}

	for _, attempt := range [][2]string{{"bob", "wrong-password"}, {"nobody", "bob-password-1"}, {"", ""}} {
		if account, err := st.VerifyAccountPassword(ctx, attempt[0], attempt[1]); account != nil || err != nil {
			t.Fatalf("sign-in %v = %+v, %v", attempt, account, err)
		}
	}
	verified, err := st.VerifyAccountPassword(ctx, " BOB ", "bob-password-1")
	if err != nil || verified == nil || verified.ID != member.ID {
		t.Fatalf("verified = %+v, %v", verified, err)
	}

	token, session, err := st.CreateAccountSession(ctx, member.ID, core.SignInMethodPassword, "test", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, AccountSessionTokenPrefix) || session.Method != core.SignInMethodPassword {
		t.Fatalf("token %q session %+v", token, session)
	}
	var stored int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM account_sessions WHERE token_hash=?`, token).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("plaintext session token stored: %d, %v", stored, err)
	}
	resolved, err := st.AuthenticateAccountSession(ctx, token)
	if err != nil || resolved == nil || resolved.ID != member.ID || resolved.LastSignInAt == nil {
		t.Fatalf("session account = %+v, %v", resolved, err)
	}
	if account, _ := st.AuthenticateAccountSession(ctx, token+"x"); account != nil {
		t.Fatal("a wrong token resolved")
	}

	// Changing one's own password keeps that session and ends the others.
	other, _, _ := st.CreateAccountSession(ctx, member.ID, core.SignInMethodPassword, "", "", time.Hour)
	if err := st.SetAccountPassword(ctx, member.ID, "bob-password-2", token); err != nil {
		t.Fatal(err)
	}
	if account, _ := st.AuthenticateAccountSession(ctx, token); account == nil {
		t.Fatal("the session that changed the password ended")
	}
	if account, _ := st.AuthenticateAccountSession(ctx, other); account != nil {
		t.Fatal("another session survived a password change")
	}
	if account, _ := st.VerifyAccountPassword(ctx, "bob", "bob-password-1"); account != nil {
		t.Fatal("the old password still works")
	}
	if account, _ := st.VerifyAccountPassword(ctx, "bob", "bob-password-2"); account == nil {
		t.Fatal("the new password does not work")
	}

	// An expired session does not resolve.
	expired, _, _ := st.CreateAccountSession(ctx, member.ID, core.SignInMethodPassword, "", "", -time.Minute)
	if account, _ := st.AuthenticateAccountSession(ctx, expired); account != nil {
		t.Fatal("an expired session resolved")
	}

	if err := st.DeleteAccountSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if account, _ := st.AuthenticateAccountSession(ctx, token); account != nil {
		t.Fatal("a signed-out session resolved")
	}
}

func TestDisablingAccountsAndLastAdmin(t *testing.T) {
	st := newTenancyStore(t)
	ctx := context.Background()
	admin, err := st.CreateFirstAdmin(ctx, NewAccount{Login: "root", Password: "root-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	member, _ := st.CreateAccount(ctx, NewAccount{Login: "bob", Password: "bob-password-1"})
	token, _, _ := st.CreateAccountSession(ctx, member.ID, core.SignInMethodPassword, "", "", time.Hour)

	disabled := core.AccountStatusDisabled
	updated, err := st.UpdateAccount(ctx, member.ID, AccountUpdate{Status: &disabled})
	if err != nil || updated.Status != disabled {
		t.Fatalf("disable = %+v, %v", updated, err)
	}
	if account, _ := st.AuthenticateAccountSession(ctx, token); account != nil {
		t.Fatal("a disabled account's session resolved")
	}
	if account, _ := st.VerifyAccountPassword(ctx, "bob", "bob-password-1"); account != nil {
		t.Fatal("a disabled account signed in")
	}
	if tenant, _ := st.GetTenant(ctx, member.TenantID); tenant.Active() {
		t.Fatal("a disabled account's namespace stayed active")
	}
	active := core.AccountStatusActive
	if _, err := st.UpdateAccount(ctx, member.ID, AccountUpdate{Status: &active}); err != nil {
		t.Fatal(err)
	}
	if tenant, _ := st.GetTenant(ctx, member.TenantID); !tenant.Active() {
		t.Fatal("re-enabling the account left its namespace disabled")
	}

	memberRole := core.AccountRoleMember
	if _, err := st.UpdateAccount(ctx, admin.ID, AccountUpdate{Role: &memberRole}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demoting the last admin err = %v", err)
	}
	if _, err := st.UpdateAccount(ctx, admin.ID, AccountUpdate{Status: &disabled}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disabling the last admin err = %v", err)
	}
	adminRole := core.AccountRoleAdmin
	if _, err := st.UpdateAccount(ctx, member.ID, AccountUpdate{Role: &adminRole}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateAccount(ctx, admin.ID, AccountUpdate{Role: &memberRole}); err != nil {
		t.Fatalf("demoting one of two admins: %v", err)
	}
	if _, err := st.UpdateAccount(ctx, "acct_missing", AccountUpdate{Role: &memberRole}); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("unknown account err = %v", err)
	}
}

func TestDeviceOwnership(t *testing.T) {
	st := newTenancyStore(t)
	ctx := context.Background()
	member, err := st.CreateAccount(ctx, NewAccount{Login: "bob", Password: "bob-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	owned, err := st.CreateOwnedDevicePairingCode(ctx, "bob's laptop", member.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	unowned, err := st.CreateDevicePairingCode(ctx, "server")
	if err != nil {
		t.Fatal(err)
	}
	bobDevice, _, err := st.RedeemDevicePairingCode(ctx, owned.Code, core.DeviceInfo{Hostname: "laptop"})
	if err != nil || bobDevice == nil || bobDevice.OwnerTenantID != member.TenantID {
		t.Fatalf("owned device = %+v, %v", bobDevice, err)
	}
	if _, _, err := st.RedeemDevicePairingCode(ctx, unowned.Code, core.DeviceInfo{Hostname: "server"}); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ListDevices(ctx)
	mine, _ := st.ListDevicesForTenant(ctx, member.TenantID)
	if len(all) != 2 || len(mine) != 1 || mine[0].ID != bobDevice.ID || mine[0].OwnerTenantID != member.TenantID {
		t.Fatalf("all = %+v, mine = %+v", all, mine)
	}
	if got, _ := st.GetDevice(ctx, bobDevice.ID); got.OwnerTenantID != member.TenantID {
		t.Fatalf("device owner = %q", got.OwnerTenantID)
	}

	// Deleting the namespace leaves the device to administrators.
	if err := st.DeleteTenant(ctx, member.TenantID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetDevice(ctx, bobDevice.ID); got.OwnerTenantID != "" {
		t.Fatalf("device owner after namespace delete = %q", got.OwnerTenantID)
	}
}

func TestAccountsOnPostgres(t *testing.T) {
	st := openPostgresIntegrationStore(t)
	ctx := context.Background()

	// Two first-run requests at once make exactly one administrator.
	results := make(chan error, 2)
	for _, login := range []string{"first", "second"} {
		go func(login string) {
			_, err := st.CreateFirstAdmin(ctx, NewAccount{Login: login, Password: login + "-password-1"})
			results <- err
		}(login)
	}
	var created, refused int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			created++
		case errors.Is(err, ErrAccountsExist):
			refused++
		default:
			t.Fatalf("first admin: %v", err)
		}
	}
	if created != 1 || refused != 1 {
		t.Fatalf("created %d, refused %d", created, refused)
	}

	member, err := st.CreateAccount(ctx, NewAccount{Login: "bob", Password: "bob-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	if account, err := st.VerifyAccountPassword(ctx, "bob", "bob-password-1"); err != nil || account == nil {
		t.Fatalf("verify = %+v, %v", account, err)
	}
	token, _, err := st.CreateAccountSession(ctx, member.ID, core.SignInMethodPassword, "agent", "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if account, err := st.AuthenticateAccountSession(ctx, token); err != nil || account == nil || account.ID != member.ID {
		t.Fatalf("session = %+v, %v", account, err)
	}
	disabled := core.AccountStatusDisabled
	if _, err := st.UpdateAccount(ctx, member.ID, AccountUpdate{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if account, _ := st.AuthenticateAccountSession(ctx, token); account != nil {
		t.Fatal("disabled account session resolved")
	}

	code, err := st.CreateOwnedDevicePairingCode(ctx, "laptop", member.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	device, _, err := st.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo{Hostname: "laptop"})
	if err != nil || device == nil {
		t.Fatalf("redeem = %+v, %v", device, err)
	}
	mine, err := st.ListDevicesForTenant(ctx, member.TenantID)
	if err != nil || len(mine) != 1 || mine[0].OwnerTenantID != member.TenantID {
		t.Fatalf("owned devices = %+v, %v", mine, err)
	}
}
