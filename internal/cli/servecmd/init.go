package servecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/amrshadid/magnetar/internal/driver"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/secret"
	"github.com/amrshadid/magnetar/internal/server"
)

// The names inside a data directory (M0 design note, part 1).
const (
	// KeyFileName is the installation key file.
	KeyFileName = "key"
	// MasterFileName is the master database, when it is SQLite.
	MasterFileName = "master.db"
	// TenantsDirName holds one SQLite file per tenant that uses SQLite.
	TenantsDirName = "tenants"
)

// EnvDataDir names the data directory when --data-dir is not given; the
// command line's client reads the same variable.
const EnvDataDir = "MAGNETAR_DATA_DIR"

// Options is what the two commands take from the process around them, so a
// test can run them with its own streams and environment.
type Options struct {
	// Stdout receives what init made; Stderr receives serve's log.
	Stdout, Stderr io.Writer
	// Getenv reads the environment; os.Getenv when nil.
	Getenv func(string) string
	// Services builds the API's handlers once the installation is open; nil
	// serves no API service (the health service and /metrics only).
	Services Services
	// Exit ends the process at once on a second stop signal; os.Exit when
	// nil.
	Exit func(code int)
}

// withDefaults fills what a caller left unset.
func (o Options) withDefaults() Options {
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Exit == nil {
		o.Exit = os.Exit
	}
	return o
}

// dataDir returns the data directory from the flag or the environment, as
// an absolute path.
func dataDir(flag string, getenv func(string) string) (string, error) {
	if flag == "" {
		flag = getenv(EnvDataDir)
	}
	if flag == "" {
		return "", fault.New(fault.Invalid, "the data directory is required: give --data-dir or set "+EnvDataDir)
	}
	abs, err := filepath.Abs(flag)
	if err != nil {
		return "", fault.Wrap(fault.Invalid, err, "the data directory "+flag+" is not a usable path")
	}
	return abs, nil
}

// masterAddress returns the master database's address: the value of the
// environment variable envName when it is given, else the SQLite file in
// the data directory. The address is never printed: it may hold a password.
func masterAddress(dir, envName string, getenv func(string) string) (address string, where string, err error) {
	if envName == "" {
		path := filepath.Join(dir, MasterFileName)
		return driver.SQLiteScheme + path, "SQLite, " + path, nil
	}
	v := getenv(envName)
	if v == "" {
		return "", "", fault.New(fault.Invalid, "--master-env names "+envName+", which is empty or not set")
	}
	return v, "the database whose address is in " + envName, nil
}

// Init returns the "init" command.
func Init(opts Options) *cobra.Command {
	opts = opts.withDefaults()
	var flags struct{ dataDir, masterEnv string }
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Make a new installation: its data directory, key file and master database",
		Long: `Make a new installation in the data directory: the directory itself (private
to you), the key file that seals every secret the installation keeps, the
master database (SQLite in the directory unless --master-env names an
environment variable holding another database's address), tenants/ and run/.

A directory that already holds an installation is refused. Losing the key
file loses every secret: back it up as soon as it is made.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := dataDir(flags.dataDir, opts.Getenv)
			if err != nil {
				return err
			}
			return runInit(cmd.Context(), opts, dir, flags.masterEnv)
		},
	}
	cmd.Flags().StringVar(&flags.dataDir, "data-dir", "", "the data directory to make (or set "+EnvDataDir+")")
	cmd.Flags().StringVar(&flags.masterEnv, "master-env", "", "the name of an environment variable holding the master database's address, when it is not SQLite in the data directory")
	return cmd
}

// installationMarks are the names whose presence means a directory already
// holds an installation, or the remains of one.
var installationMarks = []string{KeyFileName, MasterFileName, TenantsDirName, "run"}

// runInit makes the installation in dir. When a step fails, what this run
// made is removed again, so a failed init can be run again; what existed
// before is never touched.
func runInit(ctx context.Context, opts Options, dir, masterEnv string) (err error) {
	for _, name := range installationMarks {
		if _, serr := os.Lstat(filepath.Join(dir, name)); serr == nil {
			return fault.New(fault.Conflict, "the directory "+dir+" already holds an installation ("+name+" is there); init never replaces one")
		}
	}
	address, where, err := masterAddress(dir, masterEnv, opts.Getenv)
	if err != nil {
		return err
	}

	var made []string
	defer func() {
		if err != nil {
			for i := len(made) - 1; i >= 0; i-- {
				_ = os.RemoveAll(made[i])
			}
		}
	}()
	if _, serr := os.Stat(dir); errors.Is(serr, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil { //nolint:gosec // G301: the parent of the data directory is the operator's, and stays readable as such directories are; the data directory itself is private.
			return fault.Wrap(fault.Internal, err, "cannot make the parent of "+dir)
		}
		made = append(made, dir)
	}
	if err := server.EnsurePrivateDir(dir); err != nil {
		return err
	}
	keyPath := filepath.Join(dir, KeyFileName)
	if err := secret.CreateKeyFile(keyPath); err != nil {
		return err
	}
	made = append(made, keyPath)

	if masterEnv == "" {
		made = append(made, filepath.Join(dir, MasterFileName), filepath.Join(dir, MasterFileName+"-wal"), filepath.Join(dir, MasterFileName+"-shm"))
	}
	m, err := driver.OpenMaster(ctx, address)
	if err != nil {
		return err
	}
	if err := m.Migrate(ctx); err != nil {
		_ = m.Close()
		return err
	}
	if err := m.Close(); err != nil {
		return fault.Wrap(fault.Internal, err, "the master database did not close cleanly")
	}

	for _, sub := range []string{TenantsDirName, "run"} {
		p := filepath.Join(dir, sub)
		if err := server.EnsurePrivateDir(p); err != nil {
			return err
		}
		made = append(made, p)
	}

	_, err = fmt.Fprintf(opts.Stdout, `Made a new installation in %s.

The key file is %s.
It seals every secret the installation keeps, such as the passwords of tenant
databases. Losing it loses every secret, and nothing can bring them back:
copy it now to a place only you can read, and keep the copy as safe as the
file itself.

The master database is %s.

Start the server with:
  magnetar serve --data-dir %s
`, dir, keyPath, where, dir)
	if err != nil {
		// The installation is whole; only the message failed. It is not
		// undone, and the error says so.
		made = nil
		return fault.Wrap(fault.Internal, err, "the installation in "+dir+" was made, but the message about it could not be written")
	}
	return nil
}
