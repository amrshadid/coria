package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/cli/output"
	"github.com/amrshadid/coria/internal/fault"
)

// newReceiver builds the receiver noun.
func (a *app) newReceiver() *cobra.Command {
	return group("receiver", "Add, list, show, change and remove a tenant's receivers",
		`A receiver is a way for data to arrive at a tenant: a DICOM listener on one
port of the server that admits only the callers it lists (type dimse), or the
tenant's STOW-RS service for the keys it lists (type stow). Either writes what
it receives to the server's incoming area and groups it into receptions that
are archived to its storage. The ports and addresses a DIMSE receiver may use
are the installation's to allot (the settings receiver.ports and
receiver.bind-addresses).
Every command takes the tenant first. Adding, changing and removing need the
admin role on the tenant; list and show need the reader role.`,
		a.newReceiverAdd(), a.newReceiverList(), a.newReceiverShow(), a.newReceiverSet(), a.newReceiverRemove())
}

// newReceiverAdd builds "receiver add".
func (a *app) newReceiverAdd() *cobra.Command {
	var f receiverFlags
	cmd := &cobra.Command{
		Use:   "add <tenant> <name> --storage NAME [--port N] --aet TITLE [--allow-aet T | --allow-from CIDR | --any-caller] [--enabled] | --type stow --storage NAME [--allow-key ID] [--enabled]",
		Short: "Add a receiver to a tenant",
		Long: `Add a DICOM receiver to a tenant. It listens on its port only when it is
enabled (--enabled), and admits only the callers it lists: a receiver added
with no caller admits nobody, and one is open to everyone only with
--any-caller. A caller is admitted when it matches at least one rule: its
Calling AE Title (--allow-aet), the range its connection comes from
(--allow-from), or both together (--allow-aet-from TITLE@CIDR). An AE title is
what a caller says it is, so alone it identifies a caller only on a network the
site trusts.

What arrives is archived to the tenant's storage named by --storage, under
labels the naming rule makes. Without --naming-* the default rule is used, which
carries no value the sender gave. A reception is complete after --quiet-period
without a file for its study, or --max-open-time from its first file.

With --type stow the receiver is instead the tenant's STOW-RS service,
POST /tenants/<tenant>/dicom-web/studies (PS3.18 10.5): it takes the instances
stored with a key it lists (--allow-key, repeat for more), and that key must
also hold writer or more on the tenant. One added with no key takes nothing. A
key is listed by one stow receiver of a tenant at most. Each part is bounded by
--max-file-size and each request by --max-request-size.

--forward-to names a sender of the tenant (repeat for more): when a reception
is complete and archived, the server makes a send job for it to each, so what
arrives is passed on without anyone asking. It never delays a reception.

The answer says whether the receiver could start: its health is up, or down
with the reason, such as a port the server could not take.`,
		Example: `  coria storage add lab1 main --type dir --root fast
  coria receiver add lab1 scanner1 --storage main --port 11112 --aet CORIA \
      --allow-aet PRISMA1 --allow-from 10.20.0.0/24 --enabled
  coria receiver add lab1 forwarded --storage main --port 11114 --aet FWD --allow-aet PRISMA1 --forward-to pacs
  coria receiver add lab1 open --storage main --port 11113 --aet OPEN --any-caller
  coria receiver add lab1 web --type stow --storage main --allow-key 7QmXc2LpZ4 --enabled`,
		Args: exactArgs(2),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			r, _, err := f.receiver(cmd, args[1])
			if err != nil {
				return err
			}
			if f.storage == "" || (r.GetDimse() != nil && f.aet == "") {
				return fault.New(fault.Invalid, "a receiver needs --storage, and a dimse receiver --aet")
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.AddReceiverResponse, error) {
				return c.Receivers.AddReceiver(ctx, &v1.AddReceiverRequest{Tenant: args[0], Receiver: r})
			})
			if err != nil {
				return err
			}
			return messageOut(a, res, func(w io.Writer) error { return a.receiverDetails(w, res.GetReceiver()) })
		}),
	}
	f.add(cmd, false)
	return cmd
}

// spanText words a length of time in the units a flag takes: 1h, 5m, 90s.
func spanText(d *durationpb.Duration) string {
	if d == nil {
		return "-"
	}
	t := d.AsDuration()
	switch {
	case t >= time.Hour && t%time.Hour == 0:
		return strconv.FormatInt(int64(t/time.Hour), 10) + "h"
	case t >= time.Minute && t%time.Minute == 0:
		return strconv.FormatInt(int64(t/time.Minute), 10) + "m"
	}
	return strconv.FormatInt(int64(t/time.Second), 10) + "s"
}

// callerText words the callers a DIMSE receiver admits.
func callerText(d *v1.DimseReceiver) string {
	switch c := d.GetCallers().(type) {
	case *v1.DimseReceiver_AnyCaller:
		return "any caller"
	case *v1.DimseReceiver_AllowedCallers:
		var rules []string
		for _, r := range c.AllowedCallers.GetRules() {
			title, from := r.GetCallingAeTitle(), r.GetAddressRange()
			switch {
			case title != "" && from != "":
				rules = append(rules, title+" from "+from)
			case title != "":
				rules = append(rules, title)
			default:
				rules = append(rules, "from "+from)
			}
		}
		if len(rules) == 0 {
			return "nobody"
		}
		return strings.Join(rules, "; ")
	}
	return "nobody"
}

// countText words a list of UIDs a receiver accepts: the default set when it
// is empty.
func countText(uids []string) string {
	if len(uids) == 0 {
		return "the server's default set"
	}
	return strings.Join(uids, ", ")
}

// namingText words a receiver's naming rule.
func namingText(n *v1.NamingRule) string {
	if n == nil {
		return "default"
	}
	text := fmt.Sprintf("subject %s, session %s, scan %s", n.GetSubject(), n.GetSession(), n.GetScan())
	if p := n.GetPattern(); p != nil {
		text += fmt.Sprintf("; pattern %s on %s", p.GetExpression(), p.GetAttribute())
	}
	return text
}

// receiverDetails writes one receiver as labeled lines.
func (a *app) receiverDetails(w io.Writer, r *v1.Receiver) error {
	enabled := "no"
	if r.GetEnabled() {
		enabled = "yes"
	}
	typ := "dimse"
	if r.GetStow() != nil {
		typ = "stow"
	}
	pairs := []output.Pair{
		{Label: "Name", Value: r.GetName()},
		{Label: "Type", Value: typ},
		{Label: "Enabled", Value: enabled},
		{Label: "Health", Value: output.ReceiverHealth(r.GetHealth())},
	}
	if r.GetHealth().GetSince() != nil {
		pairs = append(pairs, output.Pair{Label: "Health since", Value: a.when(r.GetHealth().GetSince())})
	}
	pairs = append(pairs, output.Pair{Label: "Storage", Value: r.GetStorage()})
	if d := r.GetDimse(); d != nil {
		tls := "none (plain TCP)"
		if t := d.GetTls(); t != nil {
			tls = "certificate " + t.GetCertificateFile() + ", private key " + t.GetPrivateKeyFile()
		}
		pairs = append(pairs,
			output.Pair{Label: "Port", Value: strconv.Itoa(int(d.GetPort()))},
			output.Pair{Label: "Bind address", Value: output.Dash(d.GetBindAddress())},
			output.Pair{Label: "AE title", Value: d.GetAeTitle()},
			output.Pair{Label: "Admits", Value: callerText(d)},
			output.Pair{Label: "TLS", Value: tls},
			output.Pair{Label: "SOP classes", Value: countText(d.GetSopClasses())},
			output.Pair{Label: "Transfer syntaxes", Value: countText(d.GetTransferSyntaxes())})
	}
	if s := r.GetStow(); s != nil {
		keys := "no key"
		if len(s.GetKeyIds()) > 0 {
			keys = strings.Join(s.GetKeyIds(), ", ")
		}
		pairs = append(pairs,
			output.Pair{Label: "Takes keys", Value: keys},
			output.Pair{Label: "Max request size", Value: output.Bytes(s.GetMaxRequestBytes())})
	}
	pairs = append(pairs,
		output.Pair{Label: "Naming", Value: namingText(r.GetNaming())},
		output.Pair{Label: "Quiet period", Value: spanText(r.GetQuietPeriod())},
		output.Pair{Label: "Max open time", Value: spanText(r.GetMaxOpenTime())},
		output.Pair{Label: "Max file size", Value: output.Bytes(r.GetMaxFileBytes())})
	if len(r.GetForwardTo()) > 0 {
		pairs = append(pairs, output.Pair{Label: "Forwards to", Value: strings.Join(r.GetForwardTo(), ", ")})
	}
	pairs = append(pairs,
		output.Pair{Label: "Created", Value: a.when(r.GetCreateTime())},
		output.Pair{Label: "Updated", Value: a.when(r.GetUpdateTime())})
	return output.Details(w, a.style(), pairs)
}

// newReceiverList builds "receiver list".
func (a *app) newReceiverList() *cobra.Command {
	var l listFlags
	cmd := &cobra.Command{
		Use:   "list <tenant>",
		Short: "List a tenant's receivers with their health",
		Long: `List a tenant's receivers ordered by name, each with whether it accepts data
now: up, or down with the reason (disabled, a port in use, a certificate that
cannot be read, too little free space, and so on). A removed receiver is not
listed.`,
		Example: `  coria receiver list lab1
  coria receiver list lab1 --json`,
		Args: exactArgs(1),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			if err := l.check(); err != nil {
				return err
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			receivers, more, err := fetchPages(ctx, l, func(ctx context.Context, size int32, token string) ([]*v1.Receiver, string, error) {
				res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.ListReceiversResponse, error) {
					return c.Receivers.ListReceivers(ctx, &v1.ListReceiversRequest{Tenant: args[0], PageSize: size, PageToken: token})
				})
				if err != nil {
					return nil, "", err
				}
				return res.GetReceivers(), res.GetNextPageToken(), nil
			})
			if err != nil {
				return err
			}
			return listOut(a, "receivers", receivers, more, func() error {
				rows := make([][]string, 0, len(receivers))
				for _, r := range receivers {
					port, aet := "-", "-"
					if d := r.GetDimse(); d != nil {
						port, aet = strconv.Itoa(int(d.GetPort())), d.GetAeTitle()
					}
					rows = append(rows, []string{r.GetName(), port, aet, r.GetStorage(), output.ReceiverHealth(r.GetHealth())})
				}
				return a.write(func(w io.Writer) error {
					return output.Table(w, a.style(), []string{"NAME", "PORT", "AET", "STORAGE", "HEALTH"}, rows)
				})
			})
		}),
	}
	l.add(cmd)
	return cmd
}

// newReceiverShow builds "receiver show".
func (a *app) newReceiverShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show <tenant> <name>",
		Short: "Show one receiver with its health now",
		Long: `Show one receiver of a tenant: its port, AE title, the callers it admits, its
TLS files, its naming rule and limits, and whether it accepts data now.`,
		Example: `  coria receiver show lab1 scanner1`,
		Args:    exactArgs(2),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.GetReceiverResponse, error) {
				return c.Receivers.GetReceiver(ctx, &v1.GetReceiverRequest{Tenant: args[0], Name: args[1]})
			})
			if err != nil {
				return err
			}
			return messageOut(a, res, func(w io.Writer) error { return a.receiverDetails(w, res.GetReceiver()) })
		}),
	}
}

// newReceiverSet builds "receiver set".
func (a *app) newReceiverSet() *cobra.Command {
	var f receiverFlags
	cmd := &cobra.Command{
		Use:   "set <tenant> <name> [flags of the fields to change]",
		Short: "Change some fields of a receiver",
		Long: `Change the fields of a receiver that the flags name, and no other. A change of
the port, address, AE title, TLS, callers, SOP classes or transfer syntaxes
restarts the listener after the stores in flight finish; a change of the storage,
naming rule, quiet period, longest open time or size bound applies to what
arrives from then on.

Callers are replaced whole: --allow-aet, --allow-from and --allow-aet-from set
the list, --any-caller opens the receiver to everyone, and --no-callers closes
it. --sop-class "" and --transfer-syntax "" return to the server's default
set. A stow receiver's keys are replaced whole by --allow-key, and --no-keys
closes it. The three naming templates are given together, and --default-naming
returns to the default rule. --forward-to replaces the list of senders each
reception is sent to when it is archived, and --forward-to "" stops forwarding.
--enabled=false stops a receiver without removing it.`,
		Example: `  coria receiver set lab1 scanner1 --quiet-period 5m
  coria receiver set lab1 scanner1 --allow-aet PRISMA1 --allow-aet SKYRA2
  coria receiver set lab1 scanner1 --enabled=false
  coria receiver set lab1 scanner1 --forward-to pacs --forward-to archive2
  coria receiver set lab1 web --allow-key 7QmXc2LpZ4 --allow-key 9FvTb8RkW3`,
		Args: exactArgs(2),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			r, paths, err := f.receiver(cmd, args[1])
			if err != nil {
				return err
			}
			if len(paths) == 0 {
				return fault.New(fault.Invalid, "nothing to change: give at least one flag, such as --quiet-period")
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.SetReceiverResponse, error) {
				return c.Receivers.SetReceiver(ctx, &v1.SetReceiverRequest{Tenant: args[0], Receiver: r, UpdateMask: &fieldmaskpb.FieldMask{Paths: paths}})
			})
			if err != nil {
				return err
			}
			return messageOut(a, res, func(w io.Writer) error { return a.receiverDetails(w, res.GetReceiver()) })
		}),
	}
	f.add(cmd, true)
	return cmd
}

// newReceiverRemove builds "receiver remove".
func (a *app) newReceiverRemove() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <tenant> <name> --yes",
		Short: "Stop a receiver and remove its configuration",
		Long: `Stop a receiver, after the stores in flight finish, and remove its
configuration. Its receptions stay and those in progress go on to their end,
and its name stays held, so a name in a record always means one receiver, while
a reception or a quarantined file names it. A repeated command finds no
receiver and exits 4.

Without --yes nothing is sent: the command says what it would do and exits 2.`,
		Example: `  coria receiver remove lab1 scanner1          # says what would happen, exits 2
  coria receiver remove lab1 scanner1 --yes`,
		Args: exactArgs(2),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			if !yes {
				return fault.New(fault.Invalid, fmt.Sprintf("nothing was removed: this would stop receiver %q of tenant %q and remove its configuration; its receptions stay; run it again with --yes to do it", output.Clean(args[1]), output.Clean(args[0])))
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.RemoveReceiverResponse, error) {
				return c.Receivers.RemoveReceiver(ctx, &v1.RemoveReceiverRequest{Tenant: args[0], Name: args[1]})
			})
			if err != nil {
				return err
			}
			return messageOut(a, res, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "receiver %s of tenant %s stopped and removed; its receptions stay\n", output.Clean(args[1]), output.Clean(args[0]))
				return err
			})
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "really stop and remove the receiver; without it the command only says what it would do")
	return cmd
}
