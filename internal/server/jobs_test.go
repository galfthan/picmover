package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"picmover/internal/importer"
	"picmover/internal/testimg"
)

// live serves a fixture on a real listener (event streams need one).
type live struct {
	*fixture
	url string
}

func newLive(t *testing.T, adjust func(*Options)) *live {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	f := newFixtureWith(t, func(o *Options) {
		o.Addr = addr
		if adjust != nil {
			adjust(o)
		}
	})
	ts.Config.Handler = f.srv.Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	return &live{fixture: f, url: ts.URL}
}

func (l *live) do(method, path, body string) (int, []byte) {
	l.t.Helper()
	req, err := http.NewRequest(method, l.url+path, strings.NewReader(body))
	if err != nil {
		l.t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Picmover", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		l.t.Fatal(err)
	}
	return resp.StatusCode, b
}

func unmarshal[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

func (l *live) startJob(body string) jobJSON {
	l.t.Helper()
	code, b := l.do(http.MethodPost, "/api/l/lib/jobs", body)
	if code != http.StatusCreated {
		l.t.Fatalf("start job: %d %s", code, b)
	}
	return unmarshal[jobJSON](l.t, b)
}

type event struct {
	name string
	data string
}

// stream is an open event stream.
type stream struct {
	t      *testing.T
	resp   *http.Response
	sc     *bufio.Scanner
	cancel context.CancelFunc
	retry  string
}

func (l *live) events(id int64) *stream {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/jobs/%d/events", l.url, id), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		l.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" ||
		resp.Header.Get("Cache-Control") != "no-store" {
		cancel()
		l.t.Fatalf("events: %d %v", resp.StatusCode, resp.Header)
	}
	s := &stream{t: l.t, resp: resp, sc: bufio.NewScanner(resp.Body), cancel: cancel}
	l.t.Cleanup(s.close)
	return s
}

func (s *stream) close() {
	s.cancel()
	s.resp.Body.Close()
}

// next returns the next event, or ok=false when the stream ended.
func (s *stream) next() (event, bool) {
	var ev event
	for s.sc.Scan() {
		line := s.sc.Text()
		switch {
		case line == "":
			if ev.name != "" {
				return ev, true
			}
		case strings.HasPrefix(line, "retry: "):
			s.retry = strings.TrimPrefix(line, "retry: ")
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event: "):
			ev.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		default:
			s.t.Fatalf("unexpected line %q", line)
		}
	}
	return event{}, false
}

// rest reads events until the stream ends.
func (s *stream) rest() []event {
	var out []event
	for {
		ev, ok := s.next()
		if !ok {
			return out
		}
		out = append(out, ev)
	}
}

// importSource writes n new JPEGs, a non-media file and a copy of an
// already imported fixture photo.
func importSource(t *testing.T, base string, n int) string {
	t.Helper()
	src := filepath.Join(base, fmt.Sprintf("job-src-%d", n))
	for i := range n {
		writeFile(t, filepath.Join(src, fmt.Sprintf("new%02d.jpg", i)), testimg.JPEG(testimg.Options{Width: 40, Height: 30,
			Seed: byte(100 + i), DateTime: wall(2025, 3, 1+i%28, 12, 0, 0)}))
	}
	writeFile(t, filepath.Join(src, "readme.txt"), []byte("hello"))
	a, err := os.ReadFile(filepath.Join(base, "src", "a.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "again.jpg"), a)
	return src
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestJobImportStream(t *testing.T) {
	l := newLive(t, nil)
	src := importSource(t, l.base, 6)
	j := l.startJob(`{"kind":"import","source":` + jsonString(src) + `,"timezone":"UTC"}`)
	if j.ID != 1 || j.Kind != "import" || (j.State != "queued" && j.State != "running") || j.Source != src ||
		j.Move || j.Timezone != "UTC" || j.FinishedAt != nil || j.Error != nil || j.ExitCode != 0 {
		t.Fatalf("started job %+v", j)
	}
	if _, err := time.Parse(time.RFC3339, j.StartedAt); err != nil {
		t.Fatal(err)
	}

	s := l.events(j.ID)
	evs := s.rest()
	if s.retry != "2000" {
		t.Errorf("retry hint %q", s.retry)
	}
	if len(evs) < 3 || evs[0].name != "state" || evs[len(evs)-1].name != "done" {
		t.Fatalf("events %v", evs)
	}
	done := unmarshal[jobJSON](t, []byte(evs[len(evs)-1].data))
	if done.State != "done" || done.ExitCode != 0 || done.Error != nil || done.FinishedAt == nil || done.SessionID == nil ||
		done.Stats.Imported != 6 || done.Stats.Duplicates != 1 || done.Stats.NonMedia != 1 || done.Stats.Scanned != 8 ||
		done.Stats.ThumbsDone != 6 || done.Stats.Errors != 0 || done.Stats.BytesCopied == 0 {
		t.Fatalf("done %+v", done)
	}
	var results []resultJSON
	statuses := map[string]int{}
	for _, ev := range evs[1 : len(evs)-1] {
		switch ev.name {
		case "result":
			r := unmarshal[resultJSON](t, []byte(ev.data))
			results = append(results, r)
			statuses[r.Status]++
		case "progress", "state":
		default:
			t.Fatalf("unexpected event %v", ev)
		}
	}
	// Every result except non_media, numbered in order.
	if len(results) != 7 || statuses["imported"] != 6 || statuses["duplicate"] != 1 {
		t.Fatalf("results %+v", results)
	}
	for i, r := range results {
		if r.Seq != int64(i+1) || r.Source == "" {
			t.Fatalf("result %d: %+v", i, r)
		}
	}

	// The job object, the stream and the catalog agree.
	code, b := l.do(http.MethodGet, fmt.Sprintf("/api/jobs/%d", j.ID), "")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if got := unmarshal[jobJSON](t, b); !reflect.DeepEqual(got, done) {
		t.Fatalf("job %+v\ndone %+v", got, done)
	}
	var inSession int64
	if err := l.db.QueryRow("SELECT COUNT(*) FROM media WHERE import_session_id = ?", *done.SessionID).Scan(&inSession); err != nil {
		t.Fatal(err)
	}
	if inSession != done.Stats.Imported {
		t.Fatalf("catalog has %d rows of session %d, job imported %d", inSession, *done.SessionID, done.Stats.Imported)
	}
	if filepath.Dir(done.LogPath) != l.lib.LogDir() || !strings.HasPrefix(filepath.Base(done.LogPath), "import_") {
		t.Fatalf("log path %q", done.LogPath)
	}
	if st, err := os.Stat(done.LogPath); err != nil || st.Size() == 0 {
		t.Fatalf("log: %v", err)
	}
	// The session filter shows what this import added.
	w := l.get(fmt.Sprintf("/api/l/lib/timeline?session=%d", *done.SessionID))
	if got := decode[timelineJSON](t, w); len(got.Buckets) != 1 || got.Buckets[0] != (bucketJSON{2025, 3, 6}) {
		t.Fatalf("session timeline %s", w.Body)
	}

	// A late subscriber gets state, the ring, and done.
	late := l.events(j.ID).rest()
	if len(late) != 9 || late[0].name != "state" || late[8].name != "done" || late[0].data != late[8].data {
		t.Fatalf("late events %v", late)
	}
	for i, ev := range late[1:8] {
		if ev.name != "result" || unmarshal[resultJSON](t, []byte(ev.data)) != results[i] {
			t.Fatalf("late result %d: %v", i, ev)
		}
	}

	code, b = l.do(http.MethodGet, "/api/jobs", "")
	list := unmarshal[jobListJSON](t, b)
	if code != http.StatusOK || len(list.Jobs) != 1 || !reflect.DeepEqual(list.Jobs[0], done) {
		t.Fatalf("job list %d %s", code, b)
	}
	if code, b := l.do(http.MethodGet, "/api/jobs/99", ""); code != http.StatusNotFound || !strings.Contains(string(b), "no such job") {
		t.Fatalf("missing job: %d %s", code, b)
	}
	if code, _ := l.do(http.MethodGet, "/api/jobs/99/events", ""); code != http.StatusNotFound {
		t.Fatalf("missing job events: %d", code)
	}
}

// blocker holds the importer in its first result callback until released.
type blocker struct {
	once    sync.Once
	blocked chan struct{}
	release chan struct{}
}

func newBlocker() *blocker {
	return &blocker{blocked: make(chan struct{}), release: make(chan struct{})}
}

func (b *blocker) adjust(o *Options) {
	o.jobOptions = func(io *importer.Options) {
		next := io.OnResult
		io.Workers = 1
		io.OnResult = func(r importer.Result) {
			next(r)
			b.once.Do(func() {
				close(b.blocked)
				<-b.release
			})
		}
	}
}

func (b *blocker) wait(t *testing.T) {
	t.Helper()
	select {
	case <-b.blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("job never reached a result")
	}
}

func TestJobConflictCancelAndLateSubscriber(t *testing.T) {
	b := newBlocker()
	l := newLive(t, b.adjust)
	src := importSource(t, l.base, 8)
	j := l.startJob(`{"kind":"import","source":` + jsonString(src) + `}`)
	b.wait(t)

	code, body := l.do(http.MethodPost, "/api/l/lib/jobs", `{"kind":"thumbs"}`)
	if code != http.StatusConflict || !strings.Contains(string(body), "already running") {
		t.Fatalf("second job: %d %s", code, body)
	}

	// A subscriber joining mid-run: state (running), the ring so far
	// (the one result reported), then live events.
	s := l.events(j.ID)
	ev, _ := s.next()
	st := unmarshal[jobJSON](t, []byte(ev.data))
	if ev.name != "state" || st.State != "running" || st.Timezone != "" || st.LogPath == "" {
		t.Fatalf("first event %v", ev)
	}
	ev, _ = s.next()
	if ev.name != "result" || unmarshal[resultJSON](t, []byte(ev.data)).Seq != 1 {
		t.Fatalf("ring event %v", ev)
	}

	code, body = l.do(http.MethodPost, fmt.Sprintf("/api/jobs/%d/cancel", j.ID), "")
	if code != http.StatusAccepted || unmarshal[jobJSON](t, body).ID != j.ID {
		t.Fatalf("cancel: %d %s", code, body)
	}
	close(b.release)
	evs := s.rest()
	last := evs[len(evs)-1]
	done := unmarshal[jobJSON](t, []byte(last.data))
	if last.name != "done" || done.State != "cancelled" || done.ExitCode != 130 || done.FinishedAt == nil ||
		done.Stats.Imported >= 8 {
		t.Fatalf("after cancel: %v", evs)
	}
	for _, ev := range evs[:len(evs)-1] {
		if ev.name == "result" && unmarshal[resultJSON](t, []byte(ev.data)).Seq < 2 {
			t.Fatalf("result repeated after the ring: %v", ev)
		}
	}
	// Idempotent.
	if code, _ := l.do(http.MethodPost, fmt.Sprintf("/api/jobs/%d/cancel", j.ID), ""); code != http.StatusAccepted {
		t.Fatalf("second cancel: %d", code)
	}
	if code, _ := l.do(http.MethodPost, "/api/jobs/42/cancel", ""); code != http.StatusNotFound {
		t.Fatalf("cancel unknown: %d", code)
	}
	// The next job may start.
	j2 := l.startJob(`{"kind":"thumbs","limit":1}`)
	if j2.ID != 2 || j2.Limit != 1 {
		t.Fatalf("second job %+v", j2)
	}
	evs = l.events(j2.ID).rest()
	if d := unmarshal[jobJSON](t, []byte(evs[len(evs)-1].data)); d.State != "done" || d.ExitCode != 0 {
		t.Fatalf("thumbs job %+v", d)
	}
}

func TestJobThumbsAfterNoThumbs(t *testing.T) {
	l := newLive(t, nil)
	ctx := context.Background()
	src := importSource(t, l.base, 4)
	j := l.startJob(`{"kind":"import","source":` + jsonString(src) + `,"noThumbs":true,"minDimension":10}`)
	if !j.NoThumbs || j.MinDimension != 10 {
		t.Fatalf("job %+v", j)
	}
	evs := l.events(j.ID).rest()
	done := unmarshal[jobJSON](t, []byte(evs[len(evs)-1].data))
	if done.State != "done" || done.Stats.Imported != 4 || done.Stats.ThumbsDone != 0 {
		t.Fatalf("import %+v", done)
	}
	pending, _, _, _, err := l.db.ThumbStats(ctx)
	if err != nil || pending != 4+3 { // + the fixture's direct rows
		t.Fatalf("pending %d %v", pending, err)
	}
	// The read-only store does not have the new thumbnails yet.
	newHash := ""
	for _, ev := range evs {
		if ev.name == "result" {
			r := unmarshal[resultJSON](t, []byte(ev.data))
			if r.Status == "imported" {
				var h int64
				if err := l.db.QueryRow("SELECT hash FROM media WHERE rel_path = ?", r.Dest).Scan(&h); err != nil {
					t.Fatal(err)
				}
				newHash = hexHash(uint64(h))
				break
			}
		}
	}
	if w := l.get("/api/l/lib/thumb/" + newHash); w.Code != http.StatusNotFound {
		t.Fatalf("thumb before thumbs job: %d", w.Code)
	}

	j = l.startJob(`{"kind":"thumbs"}`)
	evs = l.events(j.ID).rest()
	done = unmarshal[jobJSON](t, []byte(evs[len(evs)-1].data))
	// The direct rows: undated decodes; outside, internal are missing files.
	if done.Kind != "thumbs" || done.Stats.ThumbsDone != 5 || done.Stats.ThumbsFailed+done.Stats.ThumbsUnsupported != 2 ||
		!strings.HasPrefix(filepath.Base(done.LogPath), "thumbs_") {
		t.Fatalf("thumbs job %+v", done)
	}
	if pending, _, _, _, _ := l.db.ThumbStats(ctx); pending != 0 {
		t.Fatalf("still pending: %d", pending)
	}
	// Served through the read-only store, which sees the committed writes.
	if w := l.get("/api/l/lib/thumb/" + newHash); w.Code != http.StatusOK {
		t.Fatalf("thumb after thumbs job: %d", w.Code)
	}
	wantExit := 0
	if done.Stats.ThumbsFailed > 0 {
		wantExit = 2
	}
	if done.ExitCode != wantExit {
		t.Fatalf("exit code %d, stats %+v", done.ExitCode, done.Stats)
	}
}

func TestJobValidation(t *testing.T) {
	l := newLive(t, nil)
	file := filepath.Join(l.base, "src", "a.jpg")
	for _, c := range []struct {
		body string
		msg  string
	}{
		{`{}`, "invalid kind"},
		{`{"kind":"export"}`, "invalid kind"},
		{`{"kind":"import"}`, "absolute"},
		{`{"kind":"import","source":"relative/dir"}`, "absolute"},
		{`{"kind":"import","source":` + jsonString(filepath.Join(l.base, "nope")) + `}`, "source"},
		{`{"kind":"import","source":` + jsonString(filepath.Join(l.lib.Root(), ".picmover")) + `}`, "picmover's own files"},
		{`{"kind":"import","source":` + jsonString(filepath.Join(l.lib.Root(), ".picmover", "logs")) + `}`, "picmover's own files"},
		{`{"kind":"import","source":` + jsonString(file) + `,"timezone":"Mars/Olympus"}`, "invalid timezone"},
		{`{"kind":"import","source":` + jsonString(file) + `,"minDimension":-1}`, "minDimension"},
		{`{"kind":"thumbs","limit":-1}`, "limit"},
		{`{"kind":"thumbs","bogus":1}`, "unknown field"},
		{`{"kind":"thumbs"} {}`, "trailing"},
		{`not json`, "invalid job"},
		{`{"kind":"import","move":"yes"}`, "invalid job"},
	} {
		code, b := l.do(http.MethodPost, "/api/l/lib/jobs", c.body)
		if code != http.StatusBadRequest || !strings.Contains(string(b), c.msg) {
			t.Errorf("%s: %d %s, want 400 %q", c.body, code, b, c.msg)
		}
	}
	// A single file is a valid source (CLI parity).
	j := l.startJob(`{"kind":"import","source":` + jsonString(file) + `,"timezone":"Europe/Helsinki"}`)
	evs := l.events(j.ID).rest()
	if d := unmarshal[jobJSON](t, []byte(evs[len(evs)-1].data)); d.State != "done" || d.Stats.Duplicates != 1 {
		t.Fatalf("file import %+v", d)
	}
}

func TestShutdownCancelsJob(t *testing.T) {
	b := newBlocker()
	l := newLive(t, b.adjust)
	src := importSource(t, l.base, 5)
	j := l.startJob(`{"kind":"import","source":` + jsonString(src) + `}`)
	b.wait(t)
	s := l.events(j.ID)
	errc := make(chan error, 1)
	go func() { errc <- l.srv.Shutdown(context.Background()) }()
	time.Sleep(50 * time.Millisecond) // Shutdown has cancelled; the job is still held
	select {
	case err := <-errc:
		t.Fatalf("Shutdown returned before the job ended: %v", err)
	default:
	}
	close(b.release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	evs := s.rest()
	if d := unmarshal[jobJSON](t, []byte(evs[len(evs)-1].data)); d.State != "cancelled" || d.ExitCode != 130 {
		t.Fatalf("after shutdown %+v", d)
	}
	if code, b := l.do(http.MethodPost, "/api/l/lib/jobs", `{"kind":"thumbs"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("job after shutdown: %d %s", code, b)
	}
}

func TestShutdownTimeoutClosesStreams(t *testing.T) {
	b := newBlocker()
	l := newLive(t, b.adjust)
	defer close(b.release)
	src := importSource(t, l.base, 3)
	j := l.startJob(`{"kind":"import","source":` + jsonString(src) + `}`)
	b.wait(t)
	s := l.events(j.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := l.srv.Shutdown(ctx); err == nil || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("Shutdown: %v", err)
	}
	// The stream ends without done.
	for _, ev := range s.rest() {
		if ev.name == "done" {
			t.Fatal("done before the job ended")
		}
	}
}

// Unit tests of the fan-out: never block the importer.

func TestSlowSubscriberDropped(t *testing.T) {
	j := &job{params: jobParams{Kind: kindImport}, state: stateRunning, created: time.Now(), done: make(chan struct{})}
	_, _, slow := j.subscribe()
	_, _, fast := j.subscribe()
	// The publisher drains fast inline after every event, so fast never
	// falls behind regardless of scheduling, while slow is never read.
	finished := make(chan struct{})
	fastGot := 0
	go func() {
		defer close(finished)
		for i := range subscriberBuffer + 10 {
			j.onResult(importer.Result{Status: importer.StatusImported, Source: fmt.Sprint(i)})
			for drained := false; !drained; {
				select {
				case _, ok := <-fast.events:
					if !ok {
						return // dropped: reported below
					}
					fastGot++
				default:
					drained = true
				}
			}
		}
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("publishing blocked on a slow subscriber")
	}
	n := 0
	for range slow.events { // closed after its buffer filled
		n++
	}
	if n != subscriberBuffer {
		t.Fatalf("slow subscriber got %d events", n)
	}
	j.mu.Lock()
	_, slowKept := j.subs[slow]
	_, fastKept := j.subs[fast]
	j.mu.Unlock()
	if slowKept || !fastKept {
		t.Fatalf("subscribers kept: slow %v fast %v", slowKept, fastKept)
	}
	if fastGot != subscriberBuffer+10 {
		t.Fatalf("fast subscriber got %d events", fastGot)
	}
	// The ring keeps the last ringSize results in order.
	_, ring, _ := j.subscribe()
	if len(ring) != ringSize || ring[0].Seq != int64(subscriberBuffer+10-ringSize+1) || ring[ringSize-1].Seq != subscriberBuffer+10 {
		t.Fatalf("ring %d [%d..%d]", len(ring), ring[0].Seq, ring[len(ring)-1].Seq)
	}
	j.onResult(importer.Result{Status: importer.StatusNonMedia})
	if _, ring, _ := j.subscribe(); ring[ringSize-1].Seq != subscriberBuffer+10 {
		t.Fatal("non_media result kept")
	}
}

func TestProgressCoalesced(t *testing.T) {
	j := &job{params: jobParams{Kind: kindImport}, state: stateRunning, created: time.Now(), done: make(chan struct{})}
	_, _, sub := j.subscribe()
	j.onProgress(importer.Stats{Scanned: 1}, 0)
	j.onProgress(importer.Stats{Scanned: 2}, 7)
	j.onProgress(importer.Stats{Scanned: 2}, 7) // unchanged: not sent
	select {
	case data := <-sub.progress:
		if p := unmarshal[progressJSON](t, data); p.Stats.Scanned != 2 {
			t.Fatalf("progress %s", data)
		}
	default:
		t.Fatal("no progress")
	}
	select {
	case data := <-sub.progress:
		t.Fatalf("stale progress %s", data)
	default:
	}
	if s := j.snapshot(); s.SessionID == nil || *s.SessionID != 7 || s.Stats.Scanned != 2 {
		t.Fatalf("snapshot %+v", s)
	}
	j.finish(stateDone, 0, importer.Stats{Scanned: 3}, 0, "")
	ev, ok := <-sub.events
	if !ok || ev.name != "done" {
		t.Fatalf("done event %v %v", ev, ok)
	}
	if _, ok := <-sub.events; ok {
		t.Fatal("stream not closed after done")
	}
}

func TestJobRetention(t *testing.T) {
	m := newJobManager(nil, "", nil, nil)
	for i := range keepFinished + 5 {
		m.jobs = append(m.jobs, &job{id: int64(i + 1)})
	}
	running := &job{id: 100}
	m.jobs = append([]*job{running}, m.jobs...)
	m.running = running
	m.trimLocked()
	if len(m.jobs) != keepFinished+1 || m.jobs[0] != running || m.jobs[1].id != 6 || m.jobs[len(m.jobs)-1].id != keepFinished+5 {
		var ids []int64
		for _, j := range m.jobs {
			ids = append(ids, j.id)
		}
		t.Fatalf("kept %v", ids)
	}
}

func TestOutcome(t *testing.T) {
	for _, c := range []struct {
		kind  string
		s     importer.Stats
		err   error
		state string
		code  int
		msg   string
	}{
		{kindImport, importer.Stats{}, nil, stateDone, 0, ""},
		{kindImport, importer.Stats{Errors: 2}, nil, stateDone, 2, "2 files could not be imported; see the job log"},
		{kindImport, importer.Stats{ThumbsFailed: 3}, nil, stateDone, 2, "thumbnails failed for 3 files; see the job log"},
		{kindImport, importer.Stats{}, fmt.Errorf("x: %w", context.Canceled), stateCancelled, 130, ""},
		{kindImport, importer.Stats{}, &importer.ThumbStageError{Err: fmt.Errorf("bad cache")}, stateDone, 2, "thumbnail stage failed (see the job log)"},
		{kindImport, importer.Stats{}, fmt.Errorf("/abs/path: disk full"), stateFailed, 1, "import failed; see the job log"},
		{kindThumbs, importer.Stats{ThumbsFailed: 1}, nil, stateDone, 2, "thumbnails failed for 1 files"},
		{kindThumbs, importer.Stats{}, context.Canceled, stateCancelled, 130, ""},
	} {
		state, code, msg := outcome(c.kind, c.s, c.err, "L", "/lib")
		if state != c.state || code != c.code || !strings.Contains(msg, c.msg) || (c.msg == "" && msg != "") ||
			strings.Contains(msg, "bad cache") || strings.Contains(msg, "/abs") || strings.Contains(msg, " L") {
			t.Errorf("%+v %v: %s %d %q", c.s, c.err, state, code, msg)
		}
	}
}
