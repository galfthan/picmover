package share

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// Scope kinds (docs/design/lan-share.md, "Scope kinds"). A new kind is a
// new value here plus its scopeAuth in internal/server; a file written by
// a newer picmover with a kind this one does not know loads as inactive
// (Scope.Known) and is written back unchanged.
const (
	KindLibraries = "libraries" // every live photo of the listed libraries, and their albums
	KindAlbum     = "album"     // the live members of one album
)

// LibraryKey is a library's stable identity within a workspace: its folder
// id and the user.db LibraryID. Both must match the served library.
type LibraryKey struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
}

// AlbumKey is an album's stable identity within its library: the id and
// the album's created_at (Unix seconds). SQLite may give a deleted
// album's id to a new album, so the id alone is not enough.
type AlbumKey struct {
	ID        int64 `json:"id"`
	CreatedAt int64 `json:"createdAt"`
}

// Scope is what a share covers. Workspace is the workspace root (links
// resolved) the libraries live in; a share is inactive while another
// root is served.
type Scope struct {
	Kind      string       `json:"kind"`
	Workspace string       `json:"workspace"`
	Libraries []LibraryKey `json:"libraries,omitempty"` // libraries kind
	Library   *LibraryKey  `json:"library,omitempty"`   // album kind
	Album     *AlbumKey    `json:"album,omitempty"`     // album kind

	// raw is the scope as read, for a kind this picmover does not know:
	// it is written back byte for byte and never authorizes anything.
	raw json.RawMessage
	// extra are members of a known kind that a newer picmover added, kept
	// on rewrite.
	extra extra
}

// Known reports whether this picmover understands the scope's kind.
func (s Scope) Known() bool { return s.Kind == KindLibraries || s.Kind == KindAlbum }

// scopeFields is Scope without its methods, for the JSON round trip.
type scopeFields struct {
	Kind      string       `json:"kind"`
	Workspace string       `json:"workspace"`
	Libraries []LibraryKey `json:"libraries,omitempty"`
	Library   *LibraryKey  `json:"library,omitempty"`
	Album     *AlbumKey    `json:"album,omitempty"`
}

// UnmarshalJSON reads a scope, keeping the raw form of an unknown kind.
func (s *Scope) UnmarshalJSON(b []byte) error {
	var f scopeFields
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*s = Scope{Kind: f.Kind, Workspace: f.Workspace, Libraries: f.Libraries, Library: f.Library, Album: f.Album}
	if !s.Known() {
		s.raw = bytes.Clone(b)
		return nil
	}
	x, err := splitExtra(b, reflect.TypeFor[scopeFields]())
	s.extra = x
	return err
}

// MarshalJSON writes the scope; an unknown kind as it was read.
func (s Scope) MarshalJSON() ([]byte, error) {
	if !s.Known() && s.raw != nil {
		return s.raw, nil
	}
	b, err := json.Marshal(scopeFields{Kind: s.Kind, Workspace: s.Workspace, Libraries: s.Libraries,
		Library: s.Library, Album: s.Album})
	if err != nil {
		return nil, err
	}
	return joinExtra(b, s.extra)
}

// ErrInvalidScope wraps every scope validation error.
var ErrInvalidScope = errors.New("invalid scope")

// maxScopeLibraries bounds the libraries of one library share.
const maxScopeLibraries = 100

// Validate checks a scope of a known kind: a workspace, and for libraries
// a non-empty list of distinct, fully identified libraries; for an album
// its library and album keys. Nothing else may be set.
func (s Scope) Validate() error {
	bad := func(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidScope, msg) }
	if s.Workspace == "" {
		return bad("no workspace")
	}
	switch s.Kind {
	case KindLibraries:
		if len(s.Libraries) == 0 {
			return bad("no libraries")
		}
		if len(s.Libraries) > maxScopeLibraries {
			return bad(fmt.Sprintf("at most %d libraries", maxScopeLibraries))
		}
		if s.Library != nil || s.Album != nil {
			return bad("a libraries scope has no album")
		}
		seen := map[string]bool{}
		for _, l := range s.Libraries {
			if l.ID == "" || l.LibraryID == "" {
				return bad("a library without its id")
			}
			if seen[l.ID] {
				return bad("library " + l.ID + " is listed twice")
			}
			seen[l.ID] = true
		}
	case KindAlbum:
		if s.Library == nil || s.Library.ID == "" || s.Library.LibraryID == "" {
			return bad("an album scope needs its library")
		}
		if s.Album == nil || s.Album.ID <= 0 || s.Album.CreatedAt <= 0 {
			return bad("an album scope needs its album")
		}
		if len(s.Libraries) > 0 {
			return bad("an album scope has one library")
		}
	default:
		return bad(fmt.Sprintf("unknown kind %q", s.Kind))
	}
	return nil
}

// Capabilities are what the phones of a share may do besides viewing
// (docs/design/lan-share.md, "Capabilities"). The server enforces them.
type Capabilities struct {
	Video    bool `json:"video"`    // play videos (file route, inline, for video rows)
	Download bool `json:"download"` // originals: images 1:1 and ?download=1
	Rate     bool `json:"rate"`     // set and see the library's ratings
	Location bool `json:"location"` // lat/lon in the detail, gps in items
}

// DefaultCapabilities are the capabilities of a new share of kind: all for
// libraries, video only for an album.
func DefaultCapabilities(kind string) Capabilities {
	if kind == KindLibraries {
		return Capabilities{Video: true, Download: true, Rate: true, Location: true}
	}
	return Capabilities{Video: true}
}
