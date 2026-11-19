package api

import "github.com/amrshadid/coria/internal/place"

// SetPortProbe makes the services of h probe a port with probe instead of
// binding it, so a test sees each port tried and on which address.
func SetPortProbe(h Handlers, probe place.Probe) { h.c.seams.probe = probe }
