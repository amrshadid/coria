package driver

import (
	"context"
	"strings"

	"github.com/amrshadid/magnetar/internal/driver/sqlite"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
)

// SQLiteScheme is the prefix of a SQLite database's address; what follows it
// is the file's path, as the operating system writes it.
const SQLiteScheme = "sqlite:"

// OpenMaster opens the master database at address. It does not migrate it;
// the caller calls Migrate. An address of no known type is Invalid, and the
// message never repeats the address.
func OpenMaster(ctx context.Context, address string) (master.Store, error) {
	if path, ok := strings.CutPrefix(address, SQLiteScheme); ok {
		return sqlite.OpenMaster(ctx, path)
	}
	return nil, fault.New(fault.Invalid, "a database address starts with sqlite:")
}
