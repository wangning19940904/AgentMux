package store

import (
	"context"
	"errors"
	"path/filepath"
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

func TestFirstAdminAndUniqueLogins(t *testing.T) {
	st := newAccountStore(t)
	ctx := context.Background()

	if count, err := st.CountAccounts(ctx); err != nil || count != 0 {
		t.Fatalf("fresh store has %d accounts, %v", count, err)
	}
	admin, err := st.CreateFirstAdmin(ctx, NewAccount{Login: " Alice ", DisplayName: "Alice", Role: "member", Password: "alice-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	if admin.Login != "alice" || admin.Role != core.AccountRoleAdmin || !admin.HasPassword {
		t.Fatalf("first admin = %+v", admin)
	}
	if _, err := st.CreateFirstAdmin(ctx, NewAccount{Login: "mallory", Password: "mallory-password"}); !errors.Is(err, ErrAccountsExist) {
		t.Fatalf("second first-admin err = %v", err)
	}

	// Logins are unique.
	if _, err := st.CreateAccount(ctx, NewAccount{Login: "ALICE", Password: "another-password"}); !errors.Is(err, ErrLoginTaken) {
		t.Fatalf("duplicate login err = %v", err)
	}
	for _, bad := range []NewAccount{{Login: "x", Password: "long-enough-pw"}, {Login: "has space", Password: "long-enough-pw"}, {Login: "bob", Password: "short"}} {
		if _, err := st.CreateAccount(ctx, bad); err == nil {
			t.Fatalf("invalid account %+v was created", bad)
		}
	}
}

func TestAccountPasswordsAndSessions(t *testing.T) {
	st := newAccountStore(t)
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
	st := newAccountStore(t)
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
	active := core.AccountStatusActive
	if _, err := st.UpdateAccount(ctx, member.ID, AccountUpdate{Status: &active}); err != nil {
		t.Fatal(err)
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
	st := newAccountStore(t)
	ctx := context.Background()
	member, err := st.CreateAccount(ctx, NewAccount{Login: "bob", Password: "bob-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	owned, err := st.CreateOwnedDevicePairingCode(ctx, "bob's laptop", member.ID)
	if err != nil {
		t.Fatal(err)
	}
	unowned, err := st.CreateDevicePairingCode(ctx, "server")
	if err != nil {
		t.Fatal(err)
	}
	bobDevice, _, err := st.RedeemDevicePairingCode(ctx, owned.Code, core.DeviceInfo{Hostname: "laptop"})
	if err != nil || bobDevice == nil || bobDevice.OwnerAccountID != member.ID {
		t.Fatalf("owned device = %+v, %v", bobDevice, err)
	}
	if _, _, err := st.RedeemDevicePairingCode(ctx, unowned.Code, core.DeviceInfo{Hostname: "server"}); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ListDevices(ctx)
	mine, _ := st.ListDevicesForAccount(ctx, member.ID)
	if len(all) != 2 || len(mine) != 1 || mine[0].ID != bobDevice.ID || mine[0].OwnerAccountID != member.ID {
		t.Fatalf("all = %+v, mine = %+v", all, mine)
	}
	if got, _ := st.GetDevice(ctx, bobDevice.ID); got.OwnerAccountID != member.ID {
		t.Fatalf("device owner = %q", got.OwnerAccountID)
	}

	// Handing the device back leaves it to administrators.
	if err := st.SetDeviceOwner(ctx, bobDevice.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetDevice(ctx, bobDevice.ID); got.OwnerAccountID != "" {
		t.Fatalf("device owner after release = %q", got.OwnerAccountID)
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

	code, err := st.CreateOwnedDevicePairingCode(ctx, "laptop", member.ID)
	if err != nil {
		t.Fatal(err)
	}
	device, _, err := st.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo{Hostname: "laptop"})
	if err != nil || device == nil {
		t.Fatalf("redeem = %+v, %v", device, err)
	}
	mine, err := st.ListDevicesForAccount(ctx, member.ID)
	if err != nil || len(mine) != 1 || mine[0].OwnerAccountID != member.ID {
		t.Fatalf("owned devices = %+v, %v", mine, err)
	}
}

func newAccountStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestPendingRegistrationsAndLoginStates(t *testing.T) {
	st := newAccountStore(t)
	ctx := context.Background()
	state := func(login string) string {
		t.Helper()
		value, err := st.AccountLoginState(ctx, login)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	// The first account is never pending, whatever is asked.
	root, err := st.CreateFirstAdmin(ctx, NewAccount{Login: "root", Password: "root-password-1", Pending: true})
	if err != nil || root.Status != core.AccountStatusActive {
		t.Fatalf("first admin = %+v, %v", root, err)
	}
	alice, err := st.CreateAccount(ctx, NewAccount{Login: "alice", Password: "alice-password-1", Pending: true})
	if err != nil || alice.Status != core.AccountStatusPending {
		t.Fatalf("registration = %+v, %v", alice, err)
	}
	if state("ROOT") != LoginRegistered || state("alice") != LoginPending || state("bob") != LoginUnregistered {
		t.Fatalf("states = %s %s %s", state("root"), state("alice"), state("bob"))
	}
	if account, _ := st.VerifyAccountPassword(ctx, "alice", "alice-password-1"); account != nil {
		t.Fatal("a pending account signed in")
	}
	if account, _ := st.CheckAccountPassword(ctx, "alice", "alice-password-1"); account == nil || account.Status != core.AccountStatusPending {
		t.Fatalf("check pending = %+v", account)
	}
	if account, _ := st.CheckAccountPassword(ctx, "alice", "wrong-password-1"); account != nil {
		t.Fatal("a wrong password was accepted")
	}
	if err := st.RejectPendingAccount(ctx, root.ID); !errors.Is(err, ErrAccountNotPending) {
		t.Fatalf("reject active = %v", err)
	}
	if err := st.RejectPendingAccount(ctx, "acct_missing"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("reject missing = %v", err)
	}
	if err := st.RejectPendingAccount(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	if state("alice") != LoginUnregistered {
		t.Fatal("a rejected registration kept its name")
	}
}

func TestAccountHandoverKeepsWhatTheReceiverHolds(t *testing.T) {
	from, to := newAccountStore(t), newAccountStore(t)
	ctx := context.Background()
	shared, err := to.CreateFirstAdmin(ctx, NewAccount{Login: "root", Password: "root-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := from.CreateFirstAdmin(ctx, NewAccount{Login: "ning", Password: "ning-password-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := from.CreateAccount(ctx, NewAccount{Login: "root", Password: "other-password-1"}); err != nil {
		t.Fatal(err)
	}
	disabled, err := from.CreateAccount(ctx, NewAccount{Login: "gone", Password: "gone-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	status := core.AccountStatusDisabled
	if _, err := from.UpdateAccount(ctx, disabled.ID, AccountUpdate{Status: &status}); err != nil {
		t.Fatal(err)
	}
	accounts, err := from.ExportAccountsForHandover(ctx)
	if err != nil || len(accounts) != 3 || accounts[0].PasswordHash == "" {
		t.Fatalf("export = %+v, %v", accounts, err)
	}
	// The receiver's own account, handed back to it, stays as it is.
	mine, _ := to.ExportAccountsForHandover(ctx)
	mine[0].Role = core.AccountRoleMember
	accounts = append(accounts, mine[0])
	result, err := to.ImportHandoverAccounts(ctx, accounts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Added, ",") != "ning,gone" || result.Known != 1 || strings.Join(result.Conflicts, ",") != "root" {
		t.Fatalf("result = %+v", result)
	}
	if root, _ := to.GetAccount(ctx, shared.ID); !root.IsAdmin() {
		t.Fatal("a handover changed an account the receiver holds")
	}
	if ning, _ := to.VerifyAccountPassword(ctx, "ning", "ning-password-1"); ning == nil || !ning.IsAdmin() {
		t.Fatalf("ning = %+v", ning)
	}
	if gone, _ := to.GetAccountByLogin(ctx, "gone"); gone == nil || gone.Active() {
		t.Fatalf("a disabled account came back active: %+v", gone)
	}
	// Handing the same accounts over again adds nothing.
	again, err := to.ImportHandoverAccounts(ctx, accounts)
	if err != nil || len(again.Added) != 0 || again.Known != 3 {
		t.Fatalf("again = %+v, %v", again, err)
	}
}

// Registration, rejection and handover on PostgreSQL, and what a standby
// says when it is asked to write accounts itself.
func TestAccountRegistrationAndHandoverOnPostgres(t *testing.T) {
	st := openPostgresIntegrationStore(t)
	ctx := context.Background()
	if _, err := st.CreateFirstAdmin(ctx, NewAccount{Login: "root", Password: "root-password-1"}); err != nil {
		t.Fatal(err)
	}
	alice, err := st.CreateAccount(ctx, NewAccount{Login: "alice", Password: "alice-password-1", Pending: true})
	if err != nil || alice.Status != core.AccountStatusPending {
		t.Fatalf("registration = %+v, %v", alice, err)
	}
	if state, err := st.AccountLoginState(ctx, "alice"); err != nil || state != LoginPending {
		t.Fatalf("state = %q, %v", state, err)
	}
	if err := st.RejectPendingAccount(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	signIn := time.Now().UTC().Truncate(time.Second)
	result, err := st.ImportHandoverAccounts(ctx, []HandoverAccount{
		{ID: "acct_from_standby", Login: "ning", Role: core.AccountRoleAdmin, Status: core.AccountStatusActive,
			PasswordHash: mustHash(t, "ning-password-1"), CreatedAt: signIn, UpdatedAt: signIn, LastSignInAt: &signIn},
		{ID: "acct_other_root", Login: "root", Role: core.AccountRoleAdmin, Status: core.AccountStatusActive},
	})
	if err != nil || strings.Join(result.Added, ",") != "ning" || strings.Join(result.Conflicts, ",") != "root" {
		t.Fatalf("import = %+v, %v", result, err)
	}
	if ning, _ := st.VerifyAccountPassword(ctx, "ning", "ning-password-1"); ning == nil || ning.LastSignInAt == nil {
		t.Fatalf("ning = %+v", ning)
	}

	// A standby's hub tables are fenced: an account write fails with the
	// read-only error the server turns into "ask the primary".
	if err := st.SetHubRole(ctx, HubRoleStandby, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.SetHubRole(context.Background(), HubRoleNone, 1) })
	_, err = st.CreateAccount(ctx, NewAccount{Login: "late", Password: "late-password-1", Pending: true})
	if err == nil || !strings.Contains(err.Error(), "25006") {
		t.Fatalf("standby write = %v", err)
	}
	if state, err := st.AccountLoginState(ctx, "ning"); err != nil || state != LoginRegistered {
		t.Fatalf("a standby still reads its copy: %q, %v", state, err)
	}
}

func mustHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
