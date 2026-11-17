package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/internal/cli/output"
)

// newSetting builds the setting noun.
func (a *app) newSetting() *cobra.Command {
	return group("setting", "Read and change settings",
		`The installation holds a default for every setting; a tenant may override some
of them. A setting is the tenant's override when it has one, else the
installation's default. Without --tenant a command acts on the installation's
defaults. A setting never holds a secret: its value is an ordinary argument and
is not recorded in the audit log. Reading needs the reader role on the scope;
changing needs the admin role.`,
		a.newSettingGet(), a.newSettingSet(), a.newSettingUnset(), a.newSettingList())
}

// settingWhere says where a setting's value comes from, in words.
func settingWhere(sc *v1.Scope) string {
	if _, ok := sc.GetKind().(*v1.Scope_Tenant); ok {
		return scopeText(sc) + " (override)"
	}
	return "installation (default)"
}

// settingDetails writes one setting as labeled lines.
func (a *app) settingDetails(w io.Writer, s *v1.Setting) error {
	return output.Details(w, a.style(), []output.Pair{
		{Label: "Name", Value: s.GetName()},
		{Label: "Value", Value: s.GetValue()},
		{Label: "Set on", Value: settingWhere(s.GetScope())},
	})
}

// newSettingGet builds "setting get".
func (a *app) newSettingGet() *cobra.Command {
	var tenant string
	cmd := &cobra.Command{
		Use:   "get <name> [--tenant NAME]",
		Short: "Show the value of a setting as it applies to a scope",
		Long: `Show a setting's value on the installation, or on a tenant: its override when
it has one, otherwise the installation's default. The answer says which of the
two the value came from.`,
		Example: `  magnetar setting get audit.retention
  magnetar setting get audit.retention --tenant lab1`,
		Args: exactArgs(1),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.GetSettingResponse, error) {
				return c.Settings.GetSetting(ctx, &v1.GetSettingRequest{Name: args[0], Scope: optionalScope(tenant)})
			})
			if err != nil {
				return err
			}
			return messageOut(a, res, func(w io.Writer) error { return a.settingDetails(w, res.GetSetting()) })
		}),
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "resolve the setting on this tenant (default: the installation)")
	return cmd
}

// newSettingSet builds "setting set".
func (a *app) newSettingSet() *cobra.Command {
	var tenant string
	cmd := &cobra.Command{
		Use:   "set <name> <value> [--tenant NAME]",
		Short: "Set the installation's default of a setting, or a tenant's override",
		Long: `Set a setting's value on the installation (its default for every tenant), or,
with --tenant, that tenant's override. The server refuses a name it does not
know and a value the setting does not accept.`,
		Example: `  magnetar setting set audit.retention 400d
  magnetar setting set audit.retention 90d --tenant lab1`,
		Args: exactArgs(2),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.SetSettingResponse, error) {
				return c.Settings.SetSetting(ctx, &v1.SetSettingRequest{Setting: &v1.Setting{Name: args[0], Value: args[1], Scope: optionalScope(tenant)}})
			})
			if err != nil {
				return err
			}
			return messageOut(a, res, func(w io.Writer) error { return a.settingDetails(w, res.GetSetting()) })
		}),
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "set an override for this tenant (default: the installation's default)")
	return cmd
}

// newSettingUnset builds "setting unset".
func (a *app) newSettingUnset() *cobra.Command {
	var tenant string
	cmd := &cobra.Command{
		Use:   "unset <name> --tenant NAME",
		Short: "Remove a tenant's override of a setting",
		Long: `Remove a tenant's override, so the installation's default applies to the tenant
again. Removing an override the tenant does not have changes nothing and
succeeds. The installation's default cannot be removed, only changed with
"setting set", so --tenant is required.`,
		Example: `  magnetar setting unset audit.retention --tenant lab1`,
		Args:    exactArgs(1),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.UnsetSettingResponse, error) {
				return c.Settings.UnsetSetting(ctx, &v1.UnsetSettingRequest{Name: args[0], Scope: optionalScope(tenant)})
			})
			if err != nil {
				return err
			}
			return messageOut(a, res, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "tenant %s has no override of %s; the installation's default applies: %s\n",
					output.Clean(tenant), output.Clean(args[0]), output.Clean(res.GetSetting().GetValue()))
				return err
			})
		}),
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "the tenant whose override is removed (required)")
	_ = cmd.MarkFlagRequired("tenant")
	return cmd
}

// newSettingList builds "setting list".
func (a *app) newSettingList() *cobra.Command {
	var tenant string
	var l listFlags
	cmd := &cobra.Command{
		Use:   "list [--tenant NAME]",
		Short: "List the settings set on the installation, or a tenant's overrides",
		Long: `List the settings set on one scope, ordered by name: on the installation, every
setting's default; on a tenant, only that tenant's overrides.`,
		Example: `  magnetar setting list
  magnetar setting list --tenant lab1 --json`,
		Args: exactArgs(0),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
			if err := l.check(); err != nil {
				return err
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			settings, more, err := fetchPages(ctx, l, func(ctx context.Context, size int32, token string) ([]*v1.Setting, string, error) {
				res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.ListSettingsResponse, error) {
					return c.Settings.ListSettings(ctx, &v1.ListSettingsRequest{Scope: optionalScope(tenant), PageSize: size, PageToken: token})
				})
				if err != nil {
					return nil, "", err
				}
				return res.GetSettings(), res.GetNextPageToken(), nil
			})
			if err != nil {
				return err
			}
			return listOut(a, "settings", settings, more, func() error {
				rows := make([][]string, 0, len(settings))
				for _, s := range settings {
					rows = append(rows, []string{s.GetName(), s.GetValue(), scopeText(s.GetScope())})
				}
				return a.write(func(w io.Writer) error {
					return output.Table(w, a.style(), []string{"NAME", "VALUE", "SCOPE"}, rows)
				})
			})
		}),
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "list this tenant's overrides (default: the installation's defaults)")
	l.add(cmd)
	return cmd
}
