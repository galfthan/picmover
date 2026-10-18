package share

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// Forward compatibility of share.json: a newer picmover of the same
// format version may add members to the settings, a share, its scope or
// a device. This picmover keeps them, byte for byte, when it rewrites the
// file, so a round trip through an older picmover loses nothing.

// extra holds the JSON members of an object that its Go type does not
// know.
type extra map[string]json.RawMessage

// knownKeys caches the JSON member names of struct types.
var knownKeys sync.Map // reflect.Type -> map[string]bool

// keysOf lists the JSON member names of struct type t (its json tags, or
// field names).
func keysOf(t reflect.Type) map[string]bool {
	if k, ok := knownKeys.Load(t); ok {
		return k.(map[string]bool)
	}
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		out[name] = true
	}
	knownKeys.Store(t, out)
	return out
}

// splitExtra returns the members of the JSON object b that struct type t
// does not know (nil when none). JSON member names match case-
// insensitively in encoding/json, so a known name in another case is not
// extra either.
func splitExtra(b []byte, t reflect.Type) (extra, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	known := keysOf(t)
	for k := range all {
		if known[k] {
			delete(all, k)
			continue
		}
		for n := range known {
			if strings.EqualFold(n, k) {
				delete(all, k)
				break
			}
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// joinExtra appends the members of x to the JSON object b, in name order.
func joinExtra(b []byte, x extra) ([]byte, error) {
	if len(x) == 0 {
		return b, nil
	}
	keys := make([]string, 0, len(x))
	for k := range x {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := slices.Clone(b[:len(b)-1]) // without the closing brace
	for _, k := range keys {
		if len(out) > 1 {
			out = append(out, ',')
		}
		name, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		out = append(append(append(out, name...), ':'), x[k]...)
	}
	return append(out, '}'), nil
}

type configJSON Config

// UnmarshalJSON reads the settings, keeping unknown members.
func (c *Config) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, (*configJSON)(c)); err != nil {
		return err
	}
	x, err := splitExtra(b, reflect.TypeFor[configJSON]())
	c.extra = x
	return err
}

// MarshalJSON writes the settings with the unknown members read.
func (c Config) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(configJSON(c))
	if err != nil {
		return nil, err
	}
	return joinExtra(b, c.extra)
}

type shareJSON Share

// UnmarshalJSON reads a share, keeping unknown members.
func (s *Share) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, (*shareJSON)(s)); err != nil {
		return err
	}
	x, err := splitExtra(b, reflect.TypeFor[shareJSON]())
	s.extra = x
	return err
}

// MarshalJSON writes a share with the unknown members read.
func (s Share) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(shareJSON(s))
	if err != nil {
		return nil, err
	}
	return joinExtra(b, s.extra)
}

type deviceJSON Device

// UnmarshalJSON reads a device, keeping unknown members.
func (d *Device) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, (*deviceJSON)(d)); err != nil {
		return err
	}
	x, err := splitExtra(b, reflect.TypeFor[deviceJSON]())
	d.extra = x
	return err
}

// MarshalJSON writes a device with the unknown members read.
func (d Device) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(deviceJSON(d))
	if err != nil {
		return nil, err
	}
	return joinExtra(b, d.extra)
}
