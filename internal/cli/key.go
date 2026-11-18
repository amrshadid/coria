package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/durationpb"

	v1 "github.com/amrshadid/magnetar/gen/magnetar/v1"
	"github.com/amrshadid/magnetar/internal/cli/output"
	"github.com/amrshadid/magnetar/internal/fault"
)

// keyLabel is a key's id with its holder, for a sentence.
func keyLabel(k *v1.Key) string { return output.KeyLabel(k) }

// keyStatus says whether a key works now: revoked, expired, or active.
func (a *app) keyStatus(k *v1.Key) string { return output.KeyStatus(k, a.env.Now()) }

// newKey builds the key noun.
func (a *app) newKey() *cobra.Command {
	return group("key", "Create, list, show and revoke keys",
		`A key is a credential held by a person or a system. A key has no rights of its
own: grants give it roles (see "magnetar grant"). These commands need the admin
role on the installation. A key's secret is shown once, when it is made, and
cannot be shown again.`,
		a.newKeyCreate(), a.newKeyList(), a.newKeyShow(), a.newKeyRevoke())
}

// keyDetails writes one key as labeled lines.
func (a *app) keyDetails(w io.Writer, k *v1.Key) error {
	return output.Details(w, a.style(), []output.Pair{
		{Label: "ID", Value: k.GetId()},
		{Label: "Holder", Value: k.GetHolder()},
		{Label: "Status", Value: a.keyStatus(k)},
		{Label: "Created", Value: a.when(k.GetCreateTime())},
		{Label: "Expires", Value: a.whenOr(k.GetExpireTime(), "never")},
		{Label: "Last used", Value: a.whenOr(k.GetLastUseTime(), "never")},
		{Label: "Revoked", Value: a.whenOr(k.GetRevokeTime(), "-")},
	})
}

// newKeyCreate builds "key create".
func (a *app) newKeyCreate() *cobra.Command {
	var holder, expires string
	var noExpiry bool
	cmd := &cobra.Command{
		Use:   "create --holder TEXT [--expires 90d | --no-expiry]",
		Short: "Make a key and show it once",
		Long: `Make a key for a person or a system and show it once.

On standard output the key is alone on its line, so a script can capture it
(KEY=$(magnetar key create --holder ci)). On standard error a note says that it
will not be shown again: the server keeps only a hash of it and cannot show it
a second time. Store it now, in a file only its holder can read, and give it
to a command through MAGNETAR_KEY or --key-file.

A key expires after 90 days unless --expires says otherwise; a key that never
expires has to be asked for with --no-expiry. With --json the whole answer,
the key included, is written to standard output.`,
		Example: `  magnetar key create --holder "nightly export"
  magnetar key create --holder ci --expires 30d
  magnetar key create --holder service-x --no-expiry`,
		Args: exactArgs(0),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
			req := &v1.CreateKeyRequest{Holder: holder}
			switch {
			case noExpiry:
				req.Expiry = &v1.CreateKeyRequest_NeverExpire{NeverExpire: true}
			case expires != "":
				d, err := parseSpan(expires)
				if err != nil {
					return fault.New(fault.Invalid, fmt.Sprintf("--expires %q is not a length of time; use a number and s, m, h, d or w, such as 90d", output.Clean(expires)))
				}
				req.Expiry = &v1.CreateKeyRequest_ExpireAfter{ExpireAfter: durationpb.New(d)}
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.CreateKeyResponse, error) {
				return c.Access.CreateKey(ctx, req)
			})
			if err != nil {
				return err
			}
			if a.opts.json {
				return messageOut(a, res, nil)
			}
			// The key, alone on its line, on standard output: the only place
			// a secret is ever printed.
			if err := a.write(func(w io.Writer) error {
				_, err := fmt.Fprintln(w, res.GetSecret())
				return err
			}); err != nil {
				return err
			}
			k := res.GetKey()
			_, _ = fmt.Fprintf(a.env.Stderr, "magnetar: key %s, expires %s\n", output.Clean(keyLabel(k)), a.whenOr(k.GetExpireTime(), "never"))
			_, _ = fmt.Fprintln(a.env.Stderr, "magnetar: this is the only time the key is shown; it cannot be shown again, so store it now")
			return nil
		}),
	}
	cmd.Flags().StringVar(&holder, "holder", "", "who holds the key: a person or a system, at most 256 bytes (required)")
	cmd.Flags().StringVar(&expires, "expires", "", "how long the key works: a number and s, m, h, d or w, such as 90d (default: the installation's setting key.default-lifetime, 90 days unless set)")
	cmd.Flags().BoolVar(&noExpiry, "no-expiry", false, "make a key that never expires")
	_ = cmd.MarkFlagRequired("holder")
	cmd.MarkFlagsMutuallyExclusive("expires", "no-expiry")
	return cmd
}

// newKeyList builds "key list".
func (a *app) newKeyList() *cobra.Command {
	var l listFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every key, revoked and expired ones included",
		Long: `List the installation's keys, oldest first, with whether each is active,
expired or revoked. The secrets are not here: the server never has them.`,
		Example: `  magnetar key list
  magnetar key list --json`,
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
			keys, more, err := fetchPages(ctx, l, func(ctx context.Context, size int32, token string) ([]*v1.Key, string, error) {
				res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.ListKeysResponse, error) {
					return c.Access.ListKeys(ctx, &v1.ListKeysRequest{PageSize: size, PageToken: token})
				})
				if err != nil {
					return nil, "", err
				}
				return res.GetKeys(), res.GetNextPageToken(), nil
			})
			if err != nil {
				return err
			}
			return listOut(a, "keys", keys, more, func() error {
				rows := make([][]string, 0, len(keys))
				for _, k := range keys {
					rows = append(rows, []string{k.GetId(), k.GetHolder(), a.keyStatus(k), a.when(k.GetCreateTime()), a.whenOr(k.GetExpireTime(), "never"), a.whenOr(k.GetLastUseTime(), "never")})
				}
				return a.write(func(w io.Writer) error {
					return output.Table(w, a.style(), []string{"ID", "HOLDER", "STATUS", "CREATED", "EXPIRES", "LAST USED"}, rows)
				})
			})
		}),
	}
	l.add(cmd)
	return cmd
}

// newKeyShow builds "key show".
func (a *app) newKeyShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one key",
		Long: `Show one key by its id: its holder, whether it works, when it was made, when it
expires, when it was last used and when it was revoked. The id is public; it is
the part of a key between "mgt_" and the second underscore.`,
		Example: `  magnetar key show k8Zp2QvXn5Rt`,
		Args:    exactArgs(1),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			if err := checkKeyID(args[0]); err != nil {
				return err
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.GetKeyResponse, error) {
				return c.Access.GetKey(ctx, &v1.GetKeyRequest{Id: args[0]})
			})
			if err != nil {
				return err
			}
			return messageOut(a, res, func(w io.Writer) error { return a.keyDetails(w, res.GetKey()) })
		}),
	}
}

// newKeyRevoke builds "key revoke".
func (a *app) newKeyRevoke() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>",
		Short: "End a key for good",
		Long: `Revoke a key: from this moment a request with it is refused. Its grants stay
listed, so the record shows what it could do, but they give nothing. Revoking a
key that is already revoked changes nothing and shows its first revoke time.
There is no way to undo it; make a new key instead.`,
		Example: `  magnetar key revoke k8Zp2QvXn5Rt`,
		Args:    exactArgs(1),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			if err := checkKeyID(args[0]); err != nil {
				return err
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.RevokeKeyResponse, error) {
				return c.Access.RevokeKey(ctx, &v1.RevokeKeyRequest{Id: args[0]})
			})
			if err != nil {
				return err
			}
			return messageOut(a, res, func(w io.Writer) error {
				k := res.GetKey()
				_, err := fmt.Fprintf(w, "key %s revoked at %s\n", output.Clean(keyLabel(k)), a.when(k.GetRevokeTime()))
				return err
			})
		}),
	}
}
