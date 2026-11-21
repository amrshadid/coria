package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	v1 "github.com/amrshadid/coria/gen/coria/v1"
	"github.com/amrshadid/coria/internal/cli/output"
	"github.com/amrshadid/coria/internal/deid/profile"
	"github.com/amrshadid/coria/internal/fault"
)

// newProfile builds the profile noun.
func (a *app) newProfile() *cobra.Command {
	return group("profile", "Make, read, try, explain and compare a tenant's de-identification profiles",
		`A profile is a versioned document of de-identification rules: for every element
of a DICOM file, whether it is kept, removed, emptied, replaced or changed
before the file is archived. A document names its bases first (the profiles the
server ships, such as annex-e/basic:2026d, PS3.15 Annex E's Basic Profile) and
then its own rules. Its file form is the JSON of the API's ProfileDocument; a
misspelled field is an error. A version never changes: creating a profile
makes version 1, and each later document under the same name the next.

A profile acts nowhere until a receiver names one of its versions
(coria receiver set <tenant> <receiver> --profile NAME@VERSION). Before that,
try it on sample files (try), see why each element is treated as it is
(explain), and see what a new version changes (diff). Nothing a trial run does
is stored; its audit entry keeps the counts as evidence that it was tried.

Every command takes the tenant first. create needs the admin role on the
tenant; the others need the operator role (a trial run answers the values of
the files it is given, and can read held files). Every command takes --json.`,
		a.newProfileCreate(), a.newProfileGet(), a.newProfileList(), a.newProfileVersions(), a.newProfileExport(),
		a.newProfileValidate(), a.newProfileDescribe(), a.newProfileTry(), a.newProfileExplain(), a.newProfileDiff())
}

// documentFlag is the flag that names a document: a file, or "-" for
// standard input.
const documentFlag = "document"

// readDocument reads a document in its file form from a file, or from
// standard input for "-", within the bound of a request.
func (a *app) readDocument(from string) (*v1.ProfileDocument, error) {
	var b []byte
	var err error
	if from == "-" {
		if a.env.Stdin == nil {
			return nil, fault.New(fault.Invalid, "--document - has no standard input to read")
		}
		b, err = io.ReadAll(io.LimitReader(a.env.Stdin, profile.MaxDocumentBytes+1))
	} else {
		var f *os.File
		f, err = os.Open(from) // #nosec G304 -- the person names the document to read.
		if err != nil {
			return nil, fault.Wrap(fault.Invalid, err, "cannot read --document "+from)
		}
		defer func() { _ = f.Close() }()
		b, err = io.ReadAll(io.LimitReader(f, profile.MaxDocumentBytes+1))
	}
	if err != nil {
		return nil, fault.Wrap(fault.Invalid, err, "cannot read --document "+from)
	}
	doc, err := profile.ParseDocument(b)
	if err != nil {
		return nil, fault.Wrap(fault.Invalid, err, strings.TrimPrefix(err.Error(), "profile: ")+" (in --document "+from+")")
	}
	return doc, nil
}

// parseVersionArg reads "NAME" (the latest) or "NAME@VERSION".
func parseVersionArg(s string) (profile.Ref, error) {
	ref, err := profile.ParseRef(s)
	if err != nil {
		return ref, fault.New(fault.Invalid, fmt.Sprintf("%q is not NAME or NAME@VERSION, such as research or research@3", output.Clean(s)))
	}
	return ref, nil
}

// fullView is the view a --full flag asks for.
func fullView(full bool) v1.ProfileView {
	if full {
		return v1.ProfileView_PROFILE_VIEW_FULL
	}
	return v1.ProfileView_PROFILE_VIEW_DOCUMENT
}

// problemsTable writes a document's problems or warnings, one a line.
func problemsTable(w io.Writer, a *app, ps []*v1.ProfileProblem) error {
	rows := make([][]string, 0, len(ps))
	for _, p := range ps {
		where := "document"
		switch {
		case p.GetRule() > 0:
			where = "rule " + strconv.Itoa(int(p.GetRule()))
		case p.GetPixelRule() > 0:
			where = "pixel rule " + strconv.Itoa(int(p.GetPixelRule()))
		}
		if p.GetRuleName() != "" {
			where += " (" + p.GetRuleName() + ")"
		}
		rows = append(rows, []string{where, output.Dash(p.GetField()), p.GetCode(), output.Clean(p.GetMessage())})
	}
	return output.Table(w, a.style(), []string{"WHERE", "FIELD", "CODE", "MESSAGE"}, rows)
}

// versionDetails writes a version as labeled lines.
func (a *app) versionDetails(w io.Writer, v *v1.ProfileVersion) error {
	pairs := []output.Pair{
		{Label: "Name", Value: v.GetName()},
		{Label: "Version", Value: strconv.Itoa(int(v.GetVersion()))},
		{Label: "Checksum", Value: v.GetChecksum()},
		{Label: "Created", Value: a.when(v.GetCreateTime())},
		{Label: "Created by key", Value: output.Dash(v.GetCreatedByKeyId())},
		{Label: "Note", Value: output.Dash(output.Clean(v.GetNote()))},
	}
	if d := v.GetDocument(); d != nil {
		pairs = append(pairs,
			output.Pair{Label: "Bases", Value: output.Dash(strings.Join(d.GetBases(), ", "))},
			output.Pair{Label: "Own rules", Value: strconv.Itoa(len(d.GetRules()))},
			output.Pair{Label: "Pixel rules", Value: strconv.Itoa(len(d.GetPixelRules()))})
	}
	if e := v.GetExpanded(); e != nil {
		pairs = append(pairs, output.Pair{Label: "Rules expanded", Value: strconv.Itoa(len(e.GetRules()))})
	}
	return output.Details(w, a.style(), pairs)
}

// newProfileCreate builds "profile create".
func (a *app) newProfileCreate() *cobra.Command {
	var document, note string
	var expect int
	var full bool
	cmd := &cobra.Command{
		Use:   "create <tenant> <name> --document FILE|- [--note TEXT] [--expect-latest N]",
		Short: "Store a document as the next version of a profile",
		Long: `Check a document, expand it (its bases copied in, every keyword resolved to its
tag, every default written out) and store it as the next version of the
profile named, version 1 for a new name. A document with a problem stores
nothing: each problem is listed with its rule, field and code, and the exit
code is 2. A document equal to the latest version stores nothing and answers
that version.

--expect-latest N stores the version only when the profile's latest is N (0
for a profile that does not exist yet), so two people who each read version 3
do not both store a version 4 unaware of each other (exit 5 otherwise).`,
		Example: `  coria profile create lab1 research --document research.json --note "basic profile, study id kept"
  coria profile create lab1 research --document research-2.json --expect-latest 1`,
		Args: exactArgs(2),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			doc, err := a.readDocument(document)
			if err != nil {
				return err
			}
			req := &v1.CreateProfileVersionRequest{Tenant: args[0], Name: args[1], Document: doc, Note: note, View: fullView(full)}
			if cmd.Flags().Changed("expect-latest") {
				e := int32(expect) // #nosec G115 -- the server refuses one out of range.
				req.ExpectedLatestVersion = &e
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.CreateProfileVersionResponse, error) {
				return c.Profiles.CreateProfileVersion(ctx, req)
			})
			if err != nil {
				return a.withProblems(err)
			}
			return messageOut(a, res, func(w io.Writer) error {
				if !res.GetCreated() {
					_, _ = fmt.Fprintf(a.env.Stderr, "coria: the document equals version %d; no version was made\n", res.GetVersion().GetVersion())
				}
				if err := a.versionDetails(w, res.GetVersion()); err != nil {
					return err
				}
				if len(res.GetWarnings()) > 0 {
					_, _ = fmt.Fprintln(w, "\nWarnings:")
					return problemsTable(w, a, res.GetWarnings())
				}
				return nil
			})
		}),
	}
	cmd.Flags().StringVar(&document, documentFlag, "", "the document's file, or - to read it from standard input")
	cmd.Flags().StringVar(&note, "note", "", "why this version is made, at most 1024 bytes")
	cmd.Flags().IntVar(&expect, "expect-latest", 0, "store only when the profile's latest version is this one (0: the profile does not exist yet)")
	cmd.Flags().BoolVar(&full, "full", false, "answer the expanded form too (with --json)")
	_ = cmd.MarkFlagRequired(documentFlag)
	return cmd
}

// withProblems writes the problems a refused document's error carries, as
// a table, before the error itself is reported; the exit code stays the
// error's (2 for a document with problems).
func (a *app) withProblems(err error) error {
	ps := problemsOfError(err)
	if len(ps) == 0 || a.opts.json {
		return err
	}
	_ = a.write(func(w io.Writer) error { return problemsTable(w, a, ps) })
	return err
}

// newProfileGet builds "profile get".
func (a *app) newProfileGet() *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "get <tenant> <name>[@<version>] [--full]",
		Short: "Show one version of a profile",
		Long: `Show one version of a profile: its checksum, when and by which key it was made,
its note, its bases and how many rules it has. Without @VERSION, the latest.
With --json the document is in the answer, and with --full the expanded form
too.`,
		Example: `  coria profile get lab1 research
  coria profile get lab1 research@2 --json --full`,
		Args: exactArgs(2),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			ref, err := parseVersionArg(args[1])
			if err != nil {
				return err
			}
			res, err := a.getVersion(ctx, cmd, args[0], ref, full)
			if err != nil {
				return err
			}
			return messageOut(a, res, func(w io.Writer) error { return a.versionDetails(w, res.GetVersion()) })
		}),
	}
	cmd.Flags().BoolVar(&full, "full", false, "answer the expanded form too")
	return cmd
}

// getVersion reads one version.
func (a *app) getVersion(ctx context.Context, cmd *cobra.Command, tenantName string, ref profile.Ref, full bool) (*v1.GetProfileVersionResponse, error) {
	c, closeClient, err := a.open(cmd)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	return call(ctx, c, cmd, func(ctx context.Context) (*v1.GetProfileVersionResponse, error) {
		return c.Profiles.GetProfileVersion(ctx, &v1.GetProfileVersionRequest{Tenant: tenantName, Name: ref.Name, Version: ref.Version, View: fullView(full)})
	})
}

// newProfileList builds "profile list".
func (a *app) newProfileList() *cobra.Command {
	var l listFlags
	cmd := &cobra.Command{
		Use:     "list <tenant>",
		Short:   "List a tenant's profiles with their latest versions",
		Long:    `List a tenant's profiles by name, each with its latest version and when it was made.`,
		Example: `  coria profile list lab1`,
		Args:    exactArgs(1),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			if err := l.check(); err != nil {
				return err
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			items, more, err := fetchPages(ctx, l, func(ctx context.Context, size int32, token string) ([]*v1.ProfileSummary, string, error) {
				res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.ListProfilesResponse, error) {
					return c.Profiles.ListProfiles(ctx, &v1.ListProfilesRequest{Tenant: args[0], PageSize: size, PageToken: token})
				})
				if err != nil {
					return nil, "", err
				}
				return res.GetProfiles(), res.GetNextPageToken(), nil
			})
			if err != nil {
				return err
			}
			return listOut(a, "profiles", items, more, func() error {
				rows := make([][]string, 0, len(items))
				for _, p := range items {
					rows = append(rows, []string{p.GetName(), strconv.Itoa(int(p.GetLatestVersion())), a.when(p.GetLatestCreateTime())})
				}
				return a.write(func(w io.Writer) error {
					return output.Table(w, a.style(), []string{"NAME", "LATEST", "CREATED"}, rows)
				})
			})
		}),
	}
	l.add(cmd)
	return cmd
}

// newProfileVersions builds "profile versions".
func (a *app) newProfileVersions() *cobra.Command {
	var l listFlags
	cmd := &cobra.Command{
		Use:     "versions <tenant> <name>",
		Short:   "List the versions of a profile, newest first",
		Long:    `List the versions of one profile, newest first, each with when and by which key it was made, its note and the start of its checksum.`,
		Example: `  coria profile versions lab1 research`,
		Args:    exactArgs(2),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			if err := l.check(); err != nil {
				return err
			}
			c, closeClient, err := a.open(cmd)
			if err != nil {
				return err
			}
			defer closeClient()
			items, more, err := fetchPages(ctx, l, func(ctx context.Context, size int32, token string) ([]*v1.ProfileVersion, string, error) {
				res, err := call(ctx, c, cmd, func(ctx context.Context) (*v1.ListProfileVersionsResponse, error) {
					return c.Profiles.ListProfileVersions(ctx, &v1.ListProfileVersionsRequest{Tenant: args[0], Name: args[1], PageSize: size, PageToken: token})
				})
				if err != nil {
					return nil, "", err
				}
				return res.GetVersions(), res.GetNextPageToken(), nil
			})
			if err != nil {
				return err
			}
			return listOut(a, "versions", items, more, func() error {
				rows := make([][]string, 0, len(items))
				for _, v := range items {
					sum := v.GetChecksum()
					if len(sum) > 12 {
						sum = sum[:12]
					}
					rows = append(rows, []string{strconv.Itoa(int(v.GetVersion())), a.when(v.GetCreateTime()), output.Dash(v.GetCreatedByKeyId()), sum, output.Dash(output.Clean(v.GetNote()))})
				}
				return a.write(func(w io.Writer) error {
					return output.Table(w, a.style(), []string{"VERSION", "CREATED", "KEY", "CHECKSUM", "NOTE"}, rows)
				})
			})
		}),
	}
	l.add(cmd)
	return cmd
}

// newProfileExport builds "profile export".
func (a *app) newProfileExport() *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "export <tenant> <name>[@<version>] [--to FILE]",
		Short: "Write a version's document in its file form",
		Long: `Write the document of a version as it was given, in its file form (indented
JSON), to standard output or to --to. Given back to "profile create" under any
name, on a server of the same language edition, it expands to the same
checksum. With --json the answer is the version, document included.`,
		Example: `  coria profile export lab1 research --to research.json
  coria profile export lab1 research@2`,
		Args: exactArgs(2),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			ref, err := parseVersionArg(args[1])
			if err != nil {
				return err
			}
			res, err := a.getVersion(ctx, cmd, args[0], ref, false)
			if err != nil {
				return err
			}
			if a.opts.json {
				return a.write(func(w io.Writer) error { return output.JSONMessage(w, res) })
			}
			text, err := profile.FormatDocument(res.GetVersion().GetDocument())
			if err != nil {
				return fault.Wrap(fault.Internal, err, "writing the document: "+err.Error())
			}
			if to == "" {
				return a.write(func(w io.Writer) error { _, err := w.Write(text); return err })
			}
			if err := os.WriteFile(to, text, 0o600); err != nil {
				return fault.Wrap(fault.Invalid, err, "cannot write --to "+to)
			}
			return nil
		}),
	}
	cmd.Flags().StringVar(&to, "to", "", "the file to write; standard output when not given")
	return cmd
}

// newProfileValidate builds "profile validate".
func (a *app) newProfileValidate() *cobra.Command {
	var document string
	cmd := &cobra.Command{
		Use:   "validate <tenant> --document FILE|-",
		Short: "Check a document without storing it",
		Long: `Check a document as "profile create" would, and store nothing. A valid document
is answered with the checksum it would have and its warnings (a rule of its own
that a base's more specific rule overrides, a rule that keeps what a base
changes); one with problems lists each with its rule, field and code, and the
exit code is 2.`,
		Example: `  coria profile validate lab1 --document research.json
  coria profile validate lab1 --document - --json`,
		Args: exactArgs(1),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			doc, err := a.readDocument(document)
			if err != nil {
				return err
			}
			res, err := a.validate(ctx, cmd, args[0], doc, false)
			if err != nil {
				return err
			}
			if err := messageOut(a, res, func(w io.Writer) error {
				if len(res.GetProblems()) > 0 {
					return problemsTable(w, a, res.GetProblems())
				}
				if err := output.Details(w, a.style(), []output.Pair{{Label: "Valid", Value: "yes"}, {Label: "Checksum", Value: res.GetChecksum()}}); err != nil {
					return err
				}
				if len(res.GetWarnings()) > 0 {
					_, _ = fmt.Fprintln(w, "\nWarnings:")
					return problemsTable(w, a, res.GetWarnings())
				}
				return nil
			}); err != nil {
				return err
			}
			if n := len(res.GetProblems()); n > 0 {
				return fault.New(fault.Invalid, fmt.Sprintf("the document has %d problem(s)", n))
			}
			return nil
		}),
	}
	cmd.Flags().StringVar(&document, documentFlag, "", "the document's file, or - to read it from standard input")
	_ = cmd.MarkFlagRequired(documentFlag)
	return cmd
}

// validate checks a document on the server.
func (a *app) validate(ctx context.Context, cmd *cobra.Command, tenantName string, doc *v1.ProfileDocument, full bool) (*v1.ValidateProfileResponse, error) {
	c, closeClient, err := a.open(cmd)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	return call(ctx, c, cmd, func(ctx context.Context) (*v1.ValidateProfileResponse, error) {
		return c.Profiles.ValidateProfile(ctx, &v1.ValidateProfileRequest{Tenant: tenantName, Document: doc, View: fullView(full)})
	})
}

// oneofText writes a selector or an action in one short line: its kind,
// and its arguments as compact JSON when it has any, such as "keyword
// PatientName" or "replace {"constant":"ANON"}".
func oneofText(m proto.Message) string {
	b, err := profile.Canonical(m)
	if err != nil {
		return "?"
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) != nil || len(obj) == 0 {
		return string(b)
	}
	var parts []string
	for k, v := range obj {
		switch {
		case string(v) == "{}":
			parts = append(parts, k)
		case len(v) > 0 && v[0] == '"':
			var s string
			_ = json.Unmarshal(v, &s)
			parts = append(parts, k+" "+s)
		default:
			parts = append(parts, k+" "+string(v))
		}
	}
	return output.Clean(strings.Join(parts, " "))
}

// describeDoc is the JSON document of "profile describe".
type describeDoc struct {
	Name     string          `json:"name,omitempty"`
	Version  int32           `json:"version,omitempty"`
	Checksum string          `json:"checksum"`
	Expanded json.RawMessage `json:"expanded"`
	Warnings json.RawMessage `json:"warnings"`
}

// newProfileDescribe builds "profile describe".
func (a *app) newProfileDescribe() *cobra.Command {
	var document string
	cmd := &cobra.Command{
		Use:   "describe <tenant> (<name>[@<version>] | --document FILE|-)",
		Short: "List the expanded rules of a version or a document, each with its origin",
		Long: `List every rule a version (or an unsaved document) runs, in order: the rules of
its bases as they were copied in, then its own, each with its origin (the base
it came from, or "own"), its selector, its action and its reason. The rule that
decides an element is the most specific selector's (a path or content item; a
tag or keyword; a private element; a private creator's block; a group; a value
pattern; a VR), and between equally specific ones the later. "profile explain" shows the
decision for each element of a file.`,
		Example: `  coria profile describe lab1 research@2
  coria profile describe lab1 --document research.json --json`,
		Args: rangeArgs(1, 2),
		RunE: a.run(func(ctx context.Context, cmd *cobra.Command, args []string) error {
			var expanded *v1.ExpandedProfile
			var warnings []*v1.ProfileProblem
			doc := describeDoc{}
			switch {
			case len(args) == 2 && document == "":
				ref, err := parseVersionArg(args[1])
				if err != nil {
					return err
				}
				res, err := a.getVersion(ctx, cmd, args[0], ref, true)
				if err != nil {
					return err
				}
				v := res.GetVersion()
				expanded, doc.Name, doc.Version, doc.Checksum = v.GetExpanded(), v.GetName(), v.GetVersion(), v.GetChecksum()
			case len(args) == 1 && document != "":
				in, err := a.readDocument(document)
				if err != nil {
					return err
				}
				res, err := a.validate(ctx, cmd, args[0], in, true)
				if err != nil {
					return err
				}
				if len(res.GetProblems()) > 0 {
					_ = a.write(func(w io.Writer) error { return problemsTable(w, a, res.GetProblems()) })
					return fault.New(fault.Invalid, fmt.Sprintf("the document has %d problem(s)", len(res.GetProblems())))
				}
				expanded, warnings, doc.Checksum = res.GetExpanded(), res.GetWarnings(), res.GetChecksum()
			default:
				return fault.New(fault.Invalid, "name a version (NAME or NAME@VERSION) or give --document, one of the two")
			}
			if a.opts.json {
				var err error
				if doc.Expanded, err = protojson.Marshal(expanded); err != nil {
					return fault.Wrap(fault.Internal, err, "writing the answer: "+err.Error())
				}
				doc.Warnings = json.RawMessage("[]")
				if len(warnings) > 0 {
					raw, err := protojson.Marshal(&v1.ProfileProblems{Problems: warnings})
					if err != nil {
						return fault.Wrap(fault.Internal, err, "writing the answer: "+err.Error())
					}
					var wrap struct {
						Problems json.RawMessage `json:"problems"`
					}
					_ = json.Unmarshal(raw, &wrap)
					doc.Warnings = wrap.Problems
				}
				return a.write(func(w io.Writer) error { return output.JSONValue(w, doc) })
			}
			rows := make([][]string, 0, len(expanded.GetRules()))
			for i, er := range expanded.GetRules() {
				r := er.GetRule()
				origin := er.GetOrigin()
				if origin == "" {
					origin = "own"
				}
				when := ""
				if n := len(r.GetWhen()); n > 0 {
					when = strconv.Itoa(n) + " condition(s)"
				}
				rows = append(rows, []string{strconv.Itoa(i + 1), origin, output.Dash(r.GetName()), oneofText(r.GetSelector()), oneofText(r.GetAction()), output.Dash(when), output.Dash(output.Clean(r.GetReason()))})
			}
			return a.write(func(w io.Writer) error {
				if err := output.Details(w, a.style(), []output.Pair{
					{Label: "Checksum", Value: doc.Checksum},
					{Label: "Bases", Value: output.Dash(strings.Join(expanded.GetBases(), ", "))},
					{Label: "Unnamed elements", Value: strings.ToLower(strings.TrimPrefix(expanded.GetUnnamed().String(), "UNNAMED_ELEMENTS_"))},
					{Label: "Largest date shift", Value: strconv.Itoa(int(expanded.GetDateShiftDays())) + " days"},
				}); err != nil {
					return err
				}
				_, _ = fmt.Fprintln(w)
				return output.Table(w, a.style(), []string{"#", "ORIGIN", "NAME", "SELECTOR", "ACTION", "WHEN", "REASON"}, rows)
			})
		}),
	}
	cmd.Flags().StringVar(&document, documentFlag, "", "an unsaved document's file, or - to read it from standard input")
	return cmd
}
