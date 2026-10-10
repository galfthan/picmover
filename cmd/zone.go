package cmd

import (
	"fmt"
	"time"
	_ "time/tzdata" // embed the zone database so IANA names resolve on Windows
)

// resolveZone returns the zone named by an IANA name, or time.Local for "".
func resolveZone(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid --timezone %q: %w", name, err)
	}
	return loc, nil
}
