package server

import (
	"net/http"
	"time"

	"picmover/internal/userdata"
)

// importJSON is an import record. Counts are the run's; items is the
// number of its items that are live rows now (like an album's count) and
// cover the newest of them (a hash, null when none is live).
type importJSON struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	StartedAt  string  `json:"startedAt"`  // RFC 3339
	FinishedAt *string `json:"finishedAt"` // null while running or never finished
	Source     string  `json:"source"`
	SourceName string  `json:"sourceName"`
	Mode       string  `json:"mode"`    // copy, move, rebuild, record
	Host       string  `json:"host"`    // "" for records backfilled from the catalog
	Outcome    string  `json:"outcome"` // running, done, cancelled, failed
	Imported   int64   `json:"imported"`
	Existing   int64   `json:"existing"`
	Duplicates int64   `json:"duplicates"`
	Skipped    int64   `json:"skipped"`
	Errors     int64   `json:"errors"`
	Bytes      int64   `json:"bytes"`
	Items      int64   `json:"items"`
	Cover      *string `json:"cover"`
}

// importRefJSON names an import in a media detail.
type importRefJSON struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	StartedAt  string `json:"startedAt"`
	Mode       string `json:"mode"`
	SourceName string `json:"sourceName"`
}

type importsJSON struct {
	Imports []importJSON `json:"imports"`
}

func rfc3339(t time.Time) string { return t.Local().Format(time.RFC3339) }

// newImport returns the import object of rec, its live item count and
// cover from the catalog.
func (s *Server) newImport(r *http.Request, l *libHandles, rec userdata.ImportRecord) (importJSON, error) {
	out := importJSON{ID: rec.ID, Name: rec.Name, StartedAt: rfc3339(rec.StartedAt), Source: rec.Source,
		SourceName: rec.SourceName, Mode: rec.Mode, Host: rec.Host, Outcome: rec.Outcome, Imported: rec.Imported,
		Existing: rec.Existing, Duplicates: rec.Duplicates, Skipped: rec.Skipped, Errors: rec.Errors, Bytes: rec.Bytes}
	if !rec.FinishedAt.IsZero() {
		f := rfc3339(rec.FinishedAt)
		out.FinishedAt = &f
	}
	hashes, err := l.user.ImportHashes(r.Context(), rec.ID)
	if err != nil || len(hashes) == 0 {
		return out, err
	}
	live, err := l.db.LiveHashes(r.Context(), hashes)
	if err != nil {
		return out, err
	}
	out.Items = int64(len(live))
	if len(live) == 0 {
		return out, nil
	}
	h, found, err := l.db.NewestOf(r.Context(), hashes)
	if err != nil || !found {
		return out, err
	}
	c := hexHash(h)
	out.Cover = &c
	return out, nil
}

func (s *Server) handleImports(w http.ResponseWriter, r *http.Request, l *libHandles) {
	recs, err := l.user.ListImports(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := importsJSON{Imports: make([]importJSON, 0, len(recs))}
	for _, rec := range recs {
		j, err := s.newImport(r, l, rec.ImportRecord)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out.Imports = append(out.Imports, j)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) writeImport(w http.ResponseWriter, r *http.Request, l *libHandles, rec userdata.ImportRecord) {
	j, err := s.newImport(r, l, rec)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request, l *libHandles) {
	id, ok := pathID(w, r, "iid", "import")
	if !ok {
		return
	}
	rec, err := l.user.GetImport(r.Context(), id)
	if err != nil {
		s.userError(w, r, err)
		return
	}
	s.writeImport(w, r, l, rec)
}

type importRenameBody struct {
	Name *string `json:"name"`
}

// handleImportRename renames an import record (trimmed, at most 200
// characters; empty clears the name) and returns it.
func (s *Server) handleImportRename(w http.ResponseWriter, r *http.Request, l *libHandles) {
	id, ok := pathID(w, r, "iid", "import")
	if !ok {
		return
	}
	var body importRenameBody
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Name == nil {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	rec, err := l.user.RenameImport(r.Context(), id, *body.Name)
	if err != nil {
		s.userError(w, r, err)
		return
	}
	s.writeImport(w, r, l, rec)
}

// handleImportDelete deletes an import record; no file and no catalog
// row is touched.
func (s *Server) handleImportDelete(w http.ResponseWriter, r *http.Request, l *libHandles) {
	id, ok := pathID(w, r, "iid", "import")
	if !ok {
		return
	}
	if err := l.user.DeleteImport(r.Context(), id); err != nil {
		s.userError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}
