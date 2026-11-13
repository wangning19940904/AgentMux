package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// The accounts command group manages the people who sign in to the Console.
// It works on the database directly, like the tenants group, so it also
// recovers a machine where every administrator lost their password.

func accountsCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "accounts",
		Short: "Manage the people who sign in to the AgentMux Console",
		Long: `Manage the people who sign in to the AgentMux Console.

With no accounts AgentMux behaves as before: the bridge token, or the local
Console when the bridge is off, is the administrator. The first account
created becomes an administrator.`,
	}
	command.AddCommand(accountsListCmd())
	command.AddCommand(accountsCreateCmd())
	command.AddCommand(accountsStatusCmd("disable", core.AccountStatusDisabled, "Disable an account; it cannot sign in and its sessions end"))
	command.AddCommand(accountsStatusCmd("enable", core.AccountStatusActive, "Enable a disabled account"))
	command.AddCommand(accountsRoleCmd())
	command.AddCommand(accountsResetPasswordCmd())
	return command
}

func accountsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTenantStore(cmd, func(st *store.Store) error {
				accounts, err := st.ListAccounts(cmd.Context())
				if err != nil {
					return err
				}
				if len(accounts) == 0 {
					cmd.Println("No accounts yet. Create the first administrator with: amux accounts create <login>")
					return nil
				}
				writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(writer, "LOGIN\tNAME\tROLE\tSTATUS\tLAST SIGN-IN\tID")
				for _, account := range accounts {
					last := "never"
					if account.LastSignInAt != nil {
						last = account.LastSignInAt.Local().Format(time.DateTime)
					}
					fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
						account.Login, account.DisplayName, account.Role, account.Status, last, account.ID)
				}
				return writer.Flush()
			})
		},
	}
}

func accountsCreateCmd() *cobra.Command {
	var displayName string
	var admin, passwordStdin bool
	command := &cobra.Command{
		Use:   "create <login>",
		Short: "Create an account and print its password",
		Long: `Create an account. The first account is always an administrator.

Without --password-stdin a password is generated and printed once; the person
should change it after signing in.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			password, generated, err := accountPassword(cmd.InOrStdin(), passwordStdin)
			if err != nil {
				return err
			}
			return withTenantStore(cmd, func(st *store.Store) error {
				count, err := st.CountAccounts(cmd.Context())
				if err != nil {
					return err
				}
				input := store.NewAccount{Login: args[0], DisplayName: displayName, Password: password, Role: core.AccountRoleMember}
				if admin {
					input.Role = core.AccountRoleAdmin
				}
				var account *core.Account
				if count == 0 {
					account, err = st.CreateFirstAdmin(cmd.Context(), input)
				} else {
					account, err = st.CreateAccount(cmd.Context(), input)
				}
				if err != nil {
					return err
				}
				cmd.Printf("Created %s account %q (%s).\n", account.Role, account.Login, account.ID)
				if count == 0 {
					cmd.Println("This is the first account, so it is an administrator. The Console now asks browsers to sign in.")
				}
				if generated {
					cmd.Printf("\n  Password: %s\n\nThis password is shown once.\n", password)
				}
				return nil
			})
		},
	}
	command.Flags().StringVar(&displayName, "name", "", "display name")
	command.Flags().BoolVar(&admin, "admin", false, "make the account an administrator")
	command.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from the first line of stdin")
	return command
}

func accountsStatusCmd(use, status, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <login>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withAccount(cmd, args[0], func(st *store.Store, account *core.Account) error {
				updated, err := st.UpdateAccount(cmd.Context(), account.ID, store.AccountUpdate{Status: &status})
				if err != nil {
					return err
				}
				cmd.Printf("Account %q is now %s.\n", updated.Login, updated.Status)
				return nil
			})
		},
	}
}

func accountsRoleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-role <login> <admin|member>",
		Short: "Make an account an administrator or a member",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			role := strings.TrimSpace(args[1])
			if role != core.AccountRoleAdmin && role != core.AccountRoleMember {
				return fmt.Errorf("role must be %s or %s", core.AccountRoleAdmin, core.AccountRoleMember)
			}
			return withAccount(cmd, args[0], func(st *store.Store, account *core.Account) error {
				updated, err := st.UpdateAccount(cmd.Context(), account.ID, store.AccountUpdate{Role: &role})
				if err != nil {
					return err
				}
				cmd.Printf("Account %q is now %s.\n", updated.Login, updated.Role)
				return nil
			})
		},
	}
}

func accountsResetPasswordCmd() *cobra.Command {
	var passwordStdin bool
	command := &cobra.Command{
		Use:   "reset-password <login>",
		Short: "Set a new password and end the account's sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			password, generated, err := accountPassword(cmd.InOrStdin(), passwordStdin)
			if err != nil {
				return err
			}
			return withAccount(cmd, args[0], func(st *store.Store, account *core.Account) error {
				if err := st.SetAccountPassword(cmd.Context(), account.ID, password, ""); err != nil {
					return err
				}
				cmd.Printf("Reset the password of %q; its sessions ended.\n", account.Login)
				if generated {
					cmd.Printf("\n  Password: %s\n\nThis password is shown once.\n", password)
				}
				return nil
			})
		},
	}
	command.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from the first line of stdin")
	return command
}

func withAccount(cmd *cobra.Command, login string, run func(*store.Store, *core.Account) error) error {
	return withTenantStore(cmd, func(st *store.Store) error {
		account, err := st.GetAccountByLogin(cmd.Context(), login)
		if err != nil {
			return err
		}
		if account == nil {
			return fmt.Errorf("no account %q", core.NormalizeLogin(login))
		}
		return run(st, account)
	})
}

// accountPassword reads a password from stdin, or generates one.
func accountPassword(stdin io.Reader, fromStdin bool) (password string, generated bool, err error) {
	if !fromStdin {
		return store.GeneratePassword(), true, nil
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	password = strings.TrimRight(line, "\r\n")
	if err := core.ValidatePassword(password); err != nil {
		return "", false, err
	}
	return password, false, nil
}
