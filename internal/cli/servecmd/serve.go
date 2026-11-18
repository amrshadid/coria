package servecmd

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/spf13/cobra"

	"github.com/amrshadid/magnetar/internal/access"
	"github.com/amrshadid/magnetar/internal/driver"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/obs"
	"github.com/amrshadid/magnetar/internal/secret"
	"github.com/amrshadid/magnetar/internal/server"
)

// Deps is what an installation's handlers are built from, once serve has
// opened it.
type Deps struct {
	// Access is the check and the management of keys and grants.
	Access *access.Access
	// Master is the master database. The server closes it at stop.
	Master master.Store
	// Keys is the installation key file, which seals and opens secrets.
	Keys *secret.Keys
	// DataDir is the data directory.
	DataDir string
	// Log is the server's logger.
	Log *slog.Logger
}

// Built is what Services returns: how to register the handlers, and the
// parts the server must close at stop.
type Built struct {
	// Register registers the API's services on the server's
	// connect.Server.
	Register func(*connect.Server)
	// Registry is the tenant registry, or nil.
	Registry server.Registry
	// HTTPRoutes are plain HTTP routes besides /metrics.
	HTTPRoutes []server.HTTPRoute
	// ToConnect maps fault kinds to API codes; nil is
	// server.DefaultToConnect.
	ToConnect func(error) *connect.Error
}

// Services builds the API's handlers over an open installation.
type Services func(Deps) (Built, error)

// serveFlags are the flags of serve.
type serveFlags struct {
	dataDir, listen, tlsCert, tlsKey, masterEnv string
	logFormat, logLevel                         string
	drain                                       time.Duration
	deadline                                    time.Duration
}

// Serve returns the "serve" command.
func Serve(opts Options) *cobra.Command {
	opts = opts.withDefaults()
	var f serveFlags
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the installation's server",
		Long: `Run the server of the installation in the data directory.

It listens on the local socket <data-dir>/run/magnetar.sock, where the caller is
the operating system user that runs it, and on the network only when --listen
is given: with TLS when --tls-cert and --tls-key are given, and without TLS on a
loopback address only. One server runs per data directory.

The first stop signal (Ctrl-C, SIGTERM) refuses new requests, lets those in
flight finish, writes the audit entries held in memory and exits 0. A second
signal exits at once.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd.Context(), opts, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.dataDir, "data-dir", "", "the installation's data directory (or set "+EnvDataDir+")")
	fl.StringVar(&f.listen, "listen", "", "the network address to serve, HOST:PORT; none unless given")
	fl.StringVar(&f.tlsCert, "tls-cert", "", "the PEM file of the network listener's certificate")
	fl.StringVar(&f.tlsKey, "tls-key", "", "the PEM file of the certificate's private key")
	fl.StringVar(&f.masterEnv, "master-env", "", "the name of an environment variable holding the master database's address, when it is not SQLite in the data directory")
	fl.StringVar(&f.logFormat, "log-format", string(obs.JSON), "json or text")
	fl.StringVar(&f.logLevel, "log-level", "info", "debug, info, warn or error")
	fl.DurationVar(&f.drain, "drain", server.DefaultDrain, "how long requests in flight may run after a stop signal")
	fl.DurationVar(&f.deadline, "default-deadline", server.DefaultDeadline, "the deadline of a call whose client sent none")
	// The default deadline is the contract's 30 seconds; changing it is for
	// tests of the deadline, so it is not shown in the help.
	_ = fl.MarkHidden("default-deadline")
	return cmd
}

// runServe opens the installation and serves until a stop signal.
func runServe(ctx context.Context, opts Options, f serveFlags) error {
	level, ok := obs.ParseLevel(f.logLevel)
	if !ok {
		return fault.New(fault.Invalid, "--log-level is debug, info, warn or error")
	}
	if f.logFormat != string(obs.JSON) && f.logFormat != string(obs.Text) {
		return fault.New(fault.Invalid, "--log-format is json or text")
	}
	log := obs.NewLogger(opts.Stderr, obs.LogOptions{Format: obs.Format(f.logFormat), Level: level})

	dir, err := dataDir(f.dataDir, opts.Getenv)
	if err != nil {
		return err
	}
	keyPath := filepath.Join(dir, KeyFileName)
	if _, err := os.Lstat(keyPath); errors.Is(err, fs.ErrNotExist) {
		return fault.New(fault.NotFound, "no installation in "+dir+" (it has no key file); make one with: magnetar init --data-dir "+dir)
	}
	// The lock first: a second server is refused before it opens the
	// master database, so two servers never migrate it at once.
	lock, err := server.TakeLock(dir)
	if err != nil {
		return err
	}
	srv, err := open(ctx, opts, f, dir, keyPath, lock, log)
	if err != nil {
		_ = lock.Release()
		return err
	}
	return serveUntilStopped(srv, opts, log)
}

// open loads the key file, opens and migrates the master database, starts
// the access package and builds the server. On failure it closes what it
// opened, except the lock, which its caller releases.
func open(ctx context.Context, opts Options, f serveFlags, dir, keyPath string, lock *server.Lock, log *slog.Logger) (srv *server.Server, err error) {
	keys, err := secret.LoadKeyFile(keyPath)
	if err != nil {
		return nil, err
	}
	if f.masterEnv == "" {
		// Without --master-env the master is the SQLite file init made. A
		// missing file means the installation was made with --master-env,
		// and opening the path would make an empty master silently.
		if _, err := os.Lstat(filepath.Join(dir, MasterFileName)); errors.Is(err, fs.ErrNotExist) {
			return nil, fault.New(fault.NotFound, "no master database at "+filepath.Join(dir, MasterFileName)+
				"; if the installation was made with --master-env, give the same flag")
		}
	}
	address, _, err := masterAddress(dir, f.masterEnv, opts.Getenv)
	if err != nil {
		return nil, err
	}
	m, err := driver.OpenMaster(ctx, address)
	if err != nil {
		return nil, err
	}
	var a *access.Access
	defer func() {
		if err != nil {
			if a != nil {
				_ = a.Close(context.Background())
			}
			_ = m.Close()
		}
	}()
	if err := m.Migrate(ctx); err != nil {
		return nil, err
	}
	a, err = access.New(access.Config{Store: AccessStore(m), Clock: time.Now, NewID: server.NewID, Log: log})
	if err != nil {
		return nil, err
	}
	built := Built{Register: func(*connect.Server) {}}
	if opts.Services != nil {
		built, err = opts.Services(Deps{Access: a, Master: m, Keys: keys, DataDir: dir, Log: log})
		if err != nil {
			return nil, err
		}
		if built.Register == nil {
			built.Register = func(*connect.Server) {}
		}
	}
	return server.New(server.Config{
		DataDir:         dir,
		Lock:            lock,
		Listen:          f.listen,
		TLSCert:         f.tlsCert,
		TLSKey:          f.tlsKey,
		Access:          a,
		Registry:        built.Registry,
		Master:          m,
		Log:             log,
		Register:        built.Register,
		HTTPRoutes:      built.HTTPRoutes,
		ToConnect:       built.ToConnect,
		Drain:           f.drain,
		DefaultDeadline: f.deadline,
	})
}

// serveUntilStopped starts the server and waits for a stop signal or a
// listener's failure; then it stops the server and returns Stop's result,
// so a clean stop exits 0. A second signal while stopping exits at once
// with status 1.
func serveUntilStopped(srv *server.Server, opts Options, log *slog.Logger) error {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	if err := srv.Start(); err != nil {
		// Nothing is served; Stop closes what open made and the lock.
		return errors.Join(err, srv.Stop())
	}
	var cause error
	select {
	case sig := <-signals:
		log.Info("server: stopping", "signal", sig.String(), "note", "a second signal exits at once")
	case cause = <-srv.Errors():
		log.Error("server: a listener failed; stopping", "error", cause)
	}
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-signals:
			log.Warn("server: second signal; exiting at once", "signal", sig.String())
			opts.Exit(1)
		case <-done:
		}
	}()
	err := srv.Stop()
	close(done)
	return errors.Join(cause, err)
}
