package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// The tenants command group manages the applications that share this AgentMux
// instance. New tenants self-register with an empty private namespace; the
// administrator uses this command group to assign or grant resources later.

func tenantsCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "tenants",
		Short: "Manage the applications (tenants) that share this AgentMux instance",
	}
	command.AddCommand(tenantsListCmd())
	command.AddCommand(tenantsAddCmd())
	command.AddCommand(tenantsDisableCmd())
	command.AddCommand(tenantsRemoveCmd())
	command.AddCommand(tenantsTokenCmd())
	command.AddCommand(tenantsRevokeCmd())
	command.AddCommand(tenantsGrantCmd())
	command.AddCommand(tenantsAssignCmd())
	return command
}

func newTenantIDSuffix() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

func openTenantStore() (*store.Store, error) {
	cfg, _, err := loadConfig(false)
	if err != nil {
		return nil, err
	}
	if flagDatabaseURL != "" {
		cfg.Database.URL = flagDatabaseURL
	}
	return openRuntimeStore(cfg)
}

// withTenantStore removes the open/close boilerplate from every subcommand.
func withTenantStore(cmd *cobra.Command, run func(st *store.Store) error) error {
	st, err := openTenantStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return run(st)
}

func tenantsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List tenants and their credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTenantStore(cmd, func(st *store.Store) error {
				tenants, err := st.ListTenants(cmd.Context())
				if err != nil {
					return err
				}
				if len(tenants) == 0 {
					cmd.Println("No tenants yet. Register one with POST /api/v1/tenancy/register or the Console.")
					return nil
				}
				writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(writer, "NAME\tKIND\tSTATUS\tTOKENS\tID")
				for _, tenant := range tenants {
					tokens, err := st.ListTenantTokens(cmd.Context(), tenant.ID)
					if err != nil {
						return err
					}
					active := 0
					for _, token := range tokens {
						if token.RevokedAt == nil {
							active++
						}
					}
					fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%s\n",
						tenant.Name, tenant.Kind, tenant.Status, active, tenant.ID)
				}
				return writer.Flush()
			})
		},
	}
}

func tenantsAddCmd() *cobra.Command {
	var kind, note string
	command := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a tenant and print its first token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTenantStore(cmd, func(st *store.Store) error {
				name := strings.TrimSpace(args[0])
				existing, err := st.GetTenantByName(cmd.Context(), name)
				if err != nil {
					return err
				}
				if existing != nil {
					return fmt.Errorf("tenant %q already exists (id %s)", name, existing.ID)
				}
				now := time.Now().UTC()
				tenant := &core.Tenant{
					ID:        "ten_" + newTenantIDSuffix(),
					Name:      name,
					Kind:      kind,
					Note:      note,
					Status:    core.TenantStatusActive,
					CreatedAt: now,
					UpdatedAt: now,
				}
				if err := st.UpsertTenant(cmd.Context(), tenant); err != nil {
					return err
				}
				token, err := st.CreateTenantToken(cmd.Context(), tenant.ID, "cli", nil)
				if err != nil {
					return err
				}
				cmd.Printf("Created tenant %q (%s).\n\n", tenant.Name, tenant.ID)
				cmd.Printf("  AGENTMUX_BRIDGE_TOKEN=%s\n\n", token.Secret)
				cmd.Println("This token is shown once. Store it in the application's environment.")
				return nil
			})
		},
	}
	command.Flags().StringVar(&kind, "kind", core.TenantKindApp, "tenant kind: app, web or service")
	command.Flags().StringVar(&note, "note", "", "free-form description")
	return command
}

func tenantsDisableCmd() *cobra.Command {
	var enable bool
	command := &cobra.Command{
		Use:   "disable <name>",
		Short: "Disable a tenant without deleting it (its tokens stop working)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTenantStore(cmd, func(st *store.Store) error {
				tenant, err := st.GetTenantByName(cmd.Context(), strings.TrimSpace(args[0]))
				if err != nil {
					return err
				}
				if tenant == nil {
					return fmt.Errorf("unknown tenant %q", args[0])
				}
				tenant.Status = core.TenantStatusDisabled
				if enable {
					tenant.Status = core.TenantStatusActive
				}
				tenant.UpdatedAt = time.Now().UTC()
				if err := st.UpsertTenant(cmd.Context(), tenant); err != nil {
					return err
				}
				cmd.Printf("Tenant %q is now %s.\n", tenant.Name, tenant.Status)
				return nil
			})
		},
	}
	command.Flags().BoolVar(&enable, "enable", false, "re-enable instead of disabling")
	return command
}

func tenantsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a tenant; the resources it owned become unassigned",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTenantStore(cmd, func(st *store.Store) error {
				tenant, err := st.GetTenantByName(cmd.Context(), strings.TrimSpace(args[0]))
				if err != nil {
					return err
				}
				if tenant == nil {
					return fmt.Errorf("unknown tenant %q", args[0])
				}
				if err := st.DeleteTenant(cmd.Context(), tenant.ID); err != nil {
					return err
				}
				cmd.Printf("Removed tenant %q. Its agents and channels are now unassigned and admin-only.\n", tenant.Name)
				return nil
			})
		},
	}
}

func tenantsTokenCmd() *cobra.Command {
	var name string
	var expiresInHours int
	command := &cobra.Command{
		Use:   "token <tenant>",
		Short: "Mint an additional token for a tenant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTenantStore(cmd, func(st *store.Store) error {
				tenant, err := st.GetTenantByName(cmd.Context(), strings.TrimSpace(args[0]))
				if err != nil {
					return err
				}
				if tenant == nil {
					return fmt.Errorf("unknown tenant %q", args[0])
				}
				var expiresAt *time.Time
				if expiresInHours > 0 {
					deadline := time.Now().UTC().Add(time.Duration(expiresInHours) * time.Hour)
					expiresAt = &deadline
				}
				token, err := st.CreateTenantToken(cmd.Context(), tenant.ID, name, expiresAt)
				if err != nil {
					return err
				}
				cmd.Printf("%s\n", token.Secret)
				return nil
			})
		},
	}
	command.Flags().StringVar(&name, "name", "cli", "label for this token")
	command.Flags().IntVar(&expiresInHours, "expires-in-hours", 0, "expiry in hours (0 means no expiry)")
	return command
}

func tenantsRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <tenant> <token-prefix>",
		Short: "Revoke one token by its displayed prefix",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTenantStore(cmd, func(st *store.Store) error {
				tenant, err := st.GetTenantByName(cmd.Context(), strings.TrimSpace(args[0]))
				if err != nil {
					return err
				}
				if tenant == nil {
					return fmt.Errorf("unknown tenant %q", args[0])
				}
				tokens, err := st.ListTenantTokens(cmd.Context(), tenant.ID)
				if err != nil {
					return err
				}
				prefix := strings.TrimSpace(args[1])
				matched := 0
				for _, token := range tokens {
					if token.RevokedAt != nil || !strings.HasPrefix(token.Prefix, prefix) {
						continue
					}
					if err := st.RevokeTenantToken(cmd.Context(), token.ID); err != nil {
						return err
					}
					matched++
				}
				if matched == 0 {
					return fmt.Errorf("no active token of %q starts with %q", tenant.Name, prefix)
				}
				cmd.Printf("Revoked %d token(s).\n", matched)
				return nil
			})
		},
	}
}

func tenantsGrantCmd() *cobra.Command {
	var level string
	var revoke bool
	command := &cobra.Command{
		Use:   "grant <tenant> <resource-type> <resource-id>",
		Short: "Grant a tenant access to a resource it does not own",
		Long: "Resource type is one of agent, channel, trigger or provider.\n" +
			"Level is read (visible), use (runnable) or manage (editable).",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTenantStore(cmd, func(st *store.Store) error {
				tenant, err := st.GetTenantByName(cmd.Context(), strings.TrimSpace(args[0]))
				if err != nil {
					return err
				}
				if tenant == nil {
					return fmt.Errorf("unknown tenant %q", args[0])
				}
				resourceType := strings.TrimSpace(args[1])
				switch resourceType {
				case core.ResourceTypeAgent, core.ResourceTypeChannel,
					core.ResourceTypeTrigger, core.ResourceTypeProvider:
				default:
					return fmt.Errorf("resource type must be agent, channel, trigger or provider")
				}
				resourceID := strings.TrimSpace(args[2])
				if revoke {
					if err := st.DeleteResourceGrant(cmd.Context(), tenant.ID, resourceType, resourceID); err != nil {
						return err
					}
					cmd.Printf("Revoked %s access for %q on %s %s.\n", level, tenant.Name, resourceType, resourceID)
					return nil
				}
				grant := &core.ResourceGrant{
					TenantID:     tenant.ID,
					ResourceType: resourceType,
					ResourceID:   resourceID,
					Level:        core.NormalizeGrantLevel(level),
				}
				if err := st.UpsertResourceGrant(cmd.Context(), grant); err != nil {
					return err
				}
				cmd.Printf("Granted %s access for %q on %s %s.\n", grant.Level, tenant.Name, resourceType, resourceID)
				return nil
			})
		},
	}
	command.Flags().StringVar(&level, "level", core.GrantLevelUse, "read, use or manage")
	command.Flags().BoolVar(&revoke, "revoke", false, "remove the grant instead of adding it")
	return command
}

func tenantsAssignCmd() *cobra.Command {
	var unassign bool
	command := &cobra.Command{
		Use:   "assign <tenant> <resource-type> <resource-id>",
		Short: "Transfer ownership of a resource to a tenant",
		Long: "Use this to adopt agents and channels that were created before\n" +
			"tenancy existed and are currently visible only to the administrator.",
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTenantStore(cmd, func(st *store.Store) error {
				var tenantID, resourceType, resourceID string
				if unassign {
					if len(args) != 2 {
						return fmt.Errorf("--unassign takes <resource-type> <resource-id>")
					}
					resourceType, resourceID = strings.TrimSpace(args[0]), strings.TrimSpace(args[1])
				} else {
					if len(args) != 3 {
						return fmt.Errorf("assign takes <tenant> <resource-type> <resource-id>")
					}
					tenant, err := st.GetTenantByName(cmd.Context(), strings.TrimSpace(args[0]))
					if err != nil {
						return err
					}
					if tenant == nil {
						return fmt.Errorf("unknown tenant %q", args[0])
					}
					tenantID = tenant.ID
					resourceType, resourceID = strings.TrimSpace(args[1]), strings.TrimSpace(args[2])
				}
				var err error
				switch resourceType {
				case core.ResourceTypeAgent:
					err = st.SetAgentInstanceOwner(cmd.Context(), resourceID, tenantID)
				case core.ResourceTypeChannel:
					err = st.SetChannelOwner(cmd.Context(), resourceID, tenantID)
				case core.ResourceTypeTrigger:
					err = st.SetTriggerOwner(cmd.Context(), resourceID, tenantID)
				default:
					return fmt.Errorf("resource type must be agent, channel or trigger")
				}
				if err != nil {
					return err
				}
				if tenantID == "" {
					cmd.Printf("%s %s is now unassigned and admin-only.\n", resourceType, resourceID)
					return nil
				}
				cmd.Printf("%s %s now belongs to %q.\n", resourceType, resourceID, args[0])
				return nil
			})
		},
	}
	command.Flags().BoolVar(&unassign, "unassign", false, "clear ownership instead of assigning it")
	return command
}
