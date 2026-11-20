# Running CORIA

[Documentation index](README.md) · [The command line](command-line.md) · [Settings](settings.md) · [Architecture](architecture.md)

For someone who installs and operates an installation. The commands are in
[the command line](command-line.md); this page is the order to use them in and
what the server needs.

## Make an installation

```sh
coria init --data-dir /var/lib/coria
```

`init` makes the data directory, the **key file** (`key`) and the master
database (`master.db`, SQLite), and prints where the key file is. The key file
seals every secret the installation keeps; losing it loses them all, so copy it
somewhere only you can read. `init` refuses a directory that already holds an
installation. The data directory also holds `tenants/` (a SQLite file per tenant),
the incoming area, and `run/` (the local socket `coria.sock` and the lock that
keeps a second server away).

To keep the master database in Postgres instead, name an environment variable
that holds its address: `MASTER_DB='postgres://...' coria init --data-dir ...
--master-env MASTER_DB`, and pass the same `--master-env` to `serve`. An address
is never an argument.

## Run the server

```sh
coria serve --data-dir /var/lib/coria
coria serve --data-dir /var/lib/coria --listen 0.0.0.0:8443 --tls-cert cert.pem --tls-key key.pem
```

Without `--listen` the server answers only on the local socket. Its flags are
`--listen`, `--tls-cert` and `--tls-key` (a renewed certificate in the same files
is picked up without a restart), `--master-env`, `--log-format json|text`,
`--log-level`, and `--drain` (how long requests in flight may run after a stop
signal, 15 seconds by default). Logs and metrics (`/metrics`) are described in
[obs](../internal/obs/doc.go). Which changes need a restart is in
[Settings](settings.md#which-changes-take-effect-without-a-restart).

The master database and each tenant's live in SQLite files in the data directory
or in Postgres. One process writes a SQLite database: the server's lock on its
data directory keeps a second server away, a second process on the same file
would be safe for the data but could wait for as long as the first keeps writing
and then fail as busy, and Postgres is the database for anything that needs
several writing processes.

## The first key

On the local socket the commands need no key: they run as the user that runs the
server. Make the first key there and give it a role, then use it over the network:

```sh
coria key create --holder "operations" --data-dir /var/lib/coria      # shown once
coria grant set <key-id> --role admin --installation --data-dir /var/lib/coria
export CORIA_KEY=cor_...                                              # on the client
coria whoami --server https://archive.example.org:8443
```

Keys, roles, networks a key may come from, and the audit log are described in
[access](../internal/access/doc.go). The roles are reader, writer, operator and
admin, on a tenant or on the installation.

## Make a tenant that receives

The installation says where data and receivers may be; the tenant then uses them
(the same steps, as API calls, are in
[Receiving through the API](api-walkthrough-reception.md)).

```sh
coria tenant create lab1
coria setting set storage.directory-roots fast=/srv/archive
coria setting set receiver.ports 11112 --tenant lab1
coria storage add lab1 main --type dir --root fast --path main
coria storage test lab1 main
coria receiver add lab1 scanner1 --storage main --port 11112 --aet CORIA \
    --allow-aet PRISMA1 --allow-from 10.20.0.0/24 --enabled
```

A tenant on Postgres is made with `--db-env VAR` or `--db-file PATH`. An S3
storage is added with `--type s3` and an endpoint the setting
`storage.s3-endpoints` lists. A receiver admits only callers it lists; open to
everyone is something you write down (`--any-caller`). A STOW-RS receiver
(`--type stow --allow-key ID`) takes HTTP requests from the keys it lists.

From here: send a study ([a reception, end to end](reception.md)), look at what
was archived with `coria catalog subjects lab1` and `coria download`
([reading the archive](reading.md)), and follow a tenant with the interactive
program (`coria`).

CORIA does not de-identify yet: what a receiver gets is archived as it was sent,
so use synthetic or public test data.
