package postgres

import (
	"testing"

	"github.com/amrshadid/magnetar/internal/master/mastertest"
)

// TestMasterContract runs the master port's contract suite on Postgres.
func TestMasterContract(t *testing.T) {
	mastertest.Run(t, openTest)
}
