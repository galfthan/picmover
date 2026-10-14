package server

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"picmover/internal/catalog"
	"picmover/internal/share"
	"picmover/internal/userdata"
)

// albumKey is the share-local {lib} key of an album share: guests never
// see the library's folder name or the album id.
const albumKey = "album"

// libRef is a library a share covers: its folder id in the served
// workspace and the user.db LibraryID it must have.
type libRef struct {
	id        string
	libraryID string
}

// Refusals of scopeAuth.bind.
var (
	// errScopeMissing: the library is not the one shared (missing, or
	// another LibraryID); in a library share that library is simply not
	// there (404).
	errScopeMissing = errors.New("not found")
	// errScopeGone: the share's whole scope is gone (an album share's
	// album deleted or its id reused, or its library missing): 403
	// {state: "gone"}.
	errScopeGone = errors.New("This album is no longer shared.")
)

// scopeAuth is one share's scope, resolved for a request
// (docs/design/lan-share.md, "Per-request authorization"). New scope
// kinds (several albums, a date range, a tag) implement it; routes never
// switch on the kind.
type scopeAuth interface {
	// library maps a share-local {lib} key to a library, or false.
	library(key string) (libRef, bool)
	// libraries lists the libraries for /api/libraries (nil: the route
	// is 404).
	libraries() []libRef
	// wholeLibrary reports whether library-level routes (library
	// summary, albums list, album filter) are allowed.
	wholeLibrary() bool
	// bind checks the acquired library l against the scope's stable
	// keys (LibraryID, an album's id and created_at): errScopeMissing or
	// errScopeGone when it is not what was shared.
	bind(ctx context.Context, l *libHandles) error
	// restrict narrows a parsed list query to the scope. A filter the
	// scope does not allow is an errFilter (400); an album filter naming
	// no album of l is errScopeMissing (404).
	restrict(ctx context.Context, l *libHandles, f *catalog.Filter, uf *userFilter) error
	// visible reports whether a live row of l is in the scope now.
	visible(ctx context.Context, l *libHandles, m catalog.Media) (bool, error)
}

// errFilter is a list filter refused by the share (400).
type errFilter struct{ msg string }

func (e *errFilter) Error() string { return e.msg }

// scopeFor returns the scopeAuth of a share; nil for a kind this
// picmover does not know (which never authorizes anything).
func scopeFor(sh *share.Share) scopeAuth {
	switch sh.Scope.Kind {
	case share.KindLibraries:
		refs := make([]libRef, 0, len(sh.Scope.Libraries))
		for _, l := range sh.Scope.Libraries {
			refs = append(refs, libRef{id: l.ID, libraryID: l.LibraryID})
		}
		return librariesScope{refs: refs}
	case share.KindAlbum:
		if sh.Scope.Library == nil || sh.Scope.Album == nil {
			return nil
		}
		return albumScope{ref: libRef{id: sh.Scope.Library.ID, libraryID: sh.Scope.Library.LibraryID},
			album: *sh.Scope.Album}
	}
	return nil
}

// checkLibraryID compares l's user.db LibraryID with the shared one.
func checkLibraryID(ctx context.Context, l *libHandles, want string) (bool, error) {
	id, err := l.user.LibraryID(ctx)
	if err != nil {
		return false, err
	}
	return want != "" && id == want, nil
}

// librariesScope covers every live photo of the listed libraries and
// their albums (read only). {lib} is the folder id.
type librariesScope struct{ refs []libRef }

func (s librariesScope) library(key string) (libRef, bool) {
	i := slices.IndexFunc(s.refs, func(r libRef) bool { return r.id == key })
	if i < 0 {
		return libRef{}, false
	}
	return s.refs[i], true
}

func (s librariesScope) libraries() []libRef { return slices.Clone(s.refs) }

func (librariesScope) wholeLibrary() bool { return true }

func (s librariesScope) bind(ctx context.Context, l *libHandles) error {
	ref, ok := s.library(l.id)
	if !ok {
		return errScopeMissing
	}
	same, err := checkLibraryID(ctx, l, ref.libraryID)
	if err != nil {
		return err
	}
	if !same {
		return errScopeMissing
	}
	return nil
}

func (librariesScope) restrict(ctx context.Context, l *libHandles, _ *catalog.Filter, uf *userFilter) error {
	if uf.album == 0 {
		return nil
	}
	// The album must be one of this library's.
	if _, err := l.user.Album(ctx, uf.album); errors.Is(err, userdata.ErrNotFound) {
		return errScopeMissing
	} else if err != nil {
		return err
	}
	return nil
}

func (librariesScope) visible(context.Context, *libHandles, catalog.Media) (bool, error) {
	return true, nil
}

// albumScope covers the live members of one album, while they are
// members. {lib} is the literal "album".
type albumScope struct {
	ref   libRef
	album share.AlbumKey
}

func (s albumScope) library(key string) (libRef, bool) {
	if key != albumKey {
		return libRef{}, false
	}
	return s.ref, true
}

func (albumScope) libraries() []libRef { return nil }

func (albumScope) wholeLibrary() bool { return false }

// bind checks the library and the album: a missing library, another
// LibraryID, a deleted album or a reused id (another created_at) make
// the share gone.
func (s albumScope) bind(ctx context.Context, l *libHandles) error {
	if l.id != s.ref.id {
		return errScopeGone
	}
	same, err := checkLibraryID(ctx, l, s.ref.libraryID)
	if err != nil {
		return err
	}
	if !same {
		return errScopeGone
	}
	a, err := l.user.Album(ctx, s.album.ID)
	switch {
	case errors.Is(err, userdata.ErrNotFound):
		return errScopeGone
	case err != nil:
		return err
	case a.CreatedAt.Unix() != s.album.CreatedAt:
		return errScopeGone
	}
	return nil
}

func (s albumScope) restrict(_ context.Context, _ *libHandles, _ *catalog.Filter, uf *userFilter) error {
	if uf.album != 0 {
		return &errFilter{msg: "invalid album: this share is one album"}
	}
	uf.album = s.album.ID
	return nil
}

func (s albumScope) visible(ctx context.Context, l *libHandles, m catalog.Media) (bool, error) {
	ok, err := l.user.AlbumHas(ctx, s.album.ID, m.Hash)
	if err != nil {
		return false, fmt.Errorf("album membership: %w", err)
	}
	return ok, nil
}
