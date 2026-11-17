package cli

import (
	"context"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/internal/cli/output"
	"github.com/amrshadid/magnetar/internal/fault"
)

// newAudit builds the audit noun.
func (a *app) newAudit() *cobra.Command {
	return group("audit", "Read the audit log",
		`Every request to the API writes one entry to an audit log, refused requests
included. What concerns the installation is in the installation's log; what
concerns a tenant is in that tenant's. Reading a log needs the admin role on its
scope, and the reading is itself recorded.`,
		a.newAuditList())
}

// methodText shortens a method's full name, "/magnetar.v1.TenantService/CreateTenant",
// to "TenantService/CreateTenant".
func methodText(m string) string {
	return strings.TrimPrefix(m, "/magnetar.v1.")
}

// whoText says who made a request: the key's id, the local operator's user, or
// nothing when the request carried neither.
func whoText(e *v1.AuditEntry) string {
	switch {
	case e.GetKeyId() != "":
		return e.GetKeyId()
	case e.GetLocalUserId() != "":
		return "local:" + e.GetLocalUserId()
	}
	return "-"
}

// newAuditList builds "audit list".
func (a *app) newAuditList() *cobra.Command {
	var tenant, keyID, outcome, since, until string
	var l listFlags
	cmd := &cobra.Command{
		Use:   "list [--tenant NAME] [--key ID] [--outcome done|refused|failed] [--since TIME] [--until TIME]",
		Short: "List audit entries, newest first",
		Long: `List the entries of the installation's audit log, or with --tenant of that
tenant's, newest first, optionally only those of one key (--key takes the key's
public id), one outcome, or a span of time.

A TIME is a date (2026-11-17, midnight in your zone), a time with its offset
(2026-11-17T09:30:00+03:00) or an age (24h, 7d: that long before now). --since
is inclusive and --until is exclusive. Times in the table are shown in your
zone with the offset.`,
		Example: `  magnetar audit list --since 24h
  magnetar audit list --outcome refused --since 2026-11-01
  magnetar audit list --tenant lab1 --key k8Zp2QvXn5Rt --json`,
		Args: exactArgs(0),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
			if err := l.check(); err != nil {
				return err
			}
			req := &v1.ListAuditEntriesRequest{Scope: optionalScope(tenant), KeyId: keyID}
			if cmd.Flags().Changed("key") {
				if err := checkKeyID(keyID); err != nil {
					return err
				}
			}
			if outcome != "" {
				o, err := parseOutcome(outcome)
				if err != nil {
					return err
				}
				req.Outcome = o
			}
			now := a.env.Now()
			var from, to *timestamppb.Timestamp
			if since != "" {
				t, err := parseTime("since", since, now, a.env.Location)
				if err != nil {
					return err
				}
				from = timestamppb.New(t)
				req.StartTime = from
			}
			if until != "" {
				t, err := parseTime("until", until, now, a.env.Location)
				if err != nil {
					return err
				}
				to = timestamppb.New(t)
				req.EndTime = to
			}
			if from != nil && to != nil && !to.AsTime().After(from.AsTime()) {
				return fault.New(fault.Invalid, "--until must be after --since")
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			entries, more, err := fetchPages(ctx, l, func(ctx context.Context, size int32, token string) ([]*v1.AuditEntry, string, error) {
				page := &v1.ListAuditEntriesRequest{
					Scope: req.GetScope(), KeyId: req.GetKeyId(), Outcome: req.GetOutcome(),
					StartTime: req.GetStartTime(), EndTime: req.GetEndTime(), PageSize: size, PageToken: token,
				}
				res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.ListAuditEntriesResponse, error) {
					return c.Audit.ListAuditEntries(ctx, page)
				})
				if err != nil {
					return nil, "", err
				}
				return res.GetEntries(), res.GetNextPageToken(), nil
			})
			if err != nil {
				return err
			}
			return listOut(a, "entries", entries, more, func() error {
				rows := make([][]string, 0, len(entries))
				for _, e := range entries {
					rows = append(rows, []string{a.when(e.GetTime()), whoText(e), methodText(e.GetMethod()), output.Dash(e.GetTenant()), output.Dash(e.GetObject()), outcomeText(e.GetOutcome()), e.GetRequestId(), output.Dash(e.GetReason())})
				}
				return a.write(func(w io.Writer) error {
					return output.Table(w, a.style(), []string{"TIME", "WHO", "METHOD", "TENANT", "OBJECT", "OUTCOME", "REQUEST ID", "REASON"}, rows)
				})
			})
		}),
	}
	cmd.Flags().StringVar(&tenant, "tenant", "", "read this tenant's log (default: the installation's log)")
	cmd.Flags().StringVar(&keyID, "key", "", "only entries of requests that carried the key with this id (the key's public id, never the key itself)")
	cmd.Flags().StringVar(&outcome, "outcome", "", "only entries with this outcome: done, refused or failed")
	cmd.Flags().StringVar(&since, "since", "", "only entries at or after this time: a date, a time with its offset, or an age such as 24h")
	cmd.Flags().StringVar(&until, "until", "", "only entries before this time, written like --since")
	_ = cmd.RegisterFlagCompletionFunc("outcome", cobra.FixedCompletions([]string{"done", "refused", "failed"}, cobra.ShellCompDirectiveNoFileComp))
	l.add(cmd)
	return cmd
}
