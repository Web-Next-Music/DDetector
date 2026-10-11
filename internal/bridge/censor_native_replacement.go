package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NativePublicationReceipt deliberately is not CensorReplacement: a local write
// must not be interpreted by the current runtime as remote publication success.
type NativePublicationReceipt struct {
	Selection        CensorSelection `json:"selection"`
	UserID           string          `json:"user_id"`
	Status           string          `json:"status"`
	LocalPath        string          `json:"local_path"`
	PublicationURL   string          `json:"publication_url"`
	GitHubPublished  bool            `json:"github_published"`
	RequestCompleted bool            `json:"request_completed"`
	Durable          bool            `json:"durable"`
}

// NativePublicationRequest keeps approval-time repository CAS evidence separate
// from OriginalURL, which native sources use for expiring analysis audio.
type NativePublicationRequest struct {
	CensorRequest
	ExpectedMapping string `json:"expected_mapping"`
}

// NativePublicationBackend is the main-wiring contract. It intentionally does
// not implement CensorBackend, whose success currently implies publication.
type NativePublicationBackend interface {
	Recover(context.Context) error
	Mapping(context.Context, int64) (string, error)
	Replace(context.Context, CensorIdentity, CensorSelection, NativePublicationRequest, CensorCandidate) (NativePublicationReceipt, error)
	ReplacementReceipt(context.Context, CensorIdentity, CensorSelection) (NativePublicationReceipt, error)
	Rollback(context.Context, CensorIdentity, CensorSelection) (NativePublicationReceipt, error)
}

var _ NativePublicationBackend = (*nativePublisher)(nil)

type nativePublisher struct {
	repo, history string
	client        *http.Client
	validate      func(context.Context, string) error
	mu            sync.Mutex
}

// NewNativePublisher requires explicit canonical repository and private runtime
// paths. Main must authorize the identity, recheck request membership/candidate
// evidence, quiesce Qt workspace operations and readers, then call Replace.
// Recovery must finish before exposing the repository. No Git commands are run.
func NewNativePublisher(repo, runtime string) (*nativePublisher, error) {
	for _, p := range []string{repo, runtime} {
		if !filepath.IsAbs(p) {
			return nil, errors.New("explicit absolute repository and runtime paths required")
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil || real != filepath.Clean(p) {
			return nil, errors.New("paths must exist and be canonical without symlinks")
		}
	}
	rel, err := filepath.Rel(repo, runtime)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, errors.New("private runtime must be outside repository")
	}
	if st, err := os.Stat(filepath.Join(repo, ".git")); err != nil || (!st.IsDir() && !st.Mode().IsRegular()) {
		return nil, errors.New("configured directory must be a repository root")
	}
	info, err := os.Stat(runtime)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("runtime must be a private directory (0700)")
	}
	h := sha256.Sum256([]byte(repo))
	history := filepath.Join(runtime, "native-publication-"+hex.EncodeToString(h[:]))
	if err := os.MkdirAll(history, 0700); err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(history)
	st, statErr := os.Stat(history)
	if err != nil || statErr != nil || real != history || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("history must be a private nonsymlink directory")
	}
	p := &nativePublisher{repo: repo, history: history, client: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	p.validate = func(ctx context.Context, path string) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-xerror", "-protocol_whitelist", "file,pipe", "-f", "mp3", "-i", path, "-map", "0:a:0", "-f", "null", "-")
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return errors.New("downloaded full MP3 failed FFmpeg validation")
		}
		return nil
	}
	return p, nil
}

type nativeFileChange struct {
	Path, Before, After string
	Existed             bool
	Delete              bool
}
type nativeJournal struct {
	Receipt    NativePublicationReceipt
	Changes    []nativeFileChange
	Done       bool
	RolledBack bool
	Provenance map[string]string `json:"provenance,omitempty"`
}

func nativeDigest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func nativeScope(i CensorIdentity, s CensorSelection) string {
	b, _ := json.Marshal([]any{i.UserID, s.RequestID, s.TrackID, s.CandidateID, s.IdempotencyKey})
	return nativeDigest(b)
}
func nativeAtomic(path string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".native-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return nativeSync(filepath.Dir(path))
}
func nativeSync(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (p *nativePublisher) lock() (func(), error) {
	// Serialize native instances while inspecting stale Qt-compatible locks.
	gate, err := openStorageLock(filepath.Join(p.history, "writer.lock"))
	if err != nil {
		return nil, err
	}
	if err = lockStorageFile(gate); err != nil {
		gate.Close()
		return nil, errors.New("another native publisher is active")
	}
	releaseGate := func() { gate.Close() }
	acquired := false
	defer func() {
		if !acquired {
			releaseGate()
		}
	}()
	path := filepath.Join(p.repo, ".ddetector-upload.lock")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil && errors.Is(err, os.ErrExist) {
		b, e := os.ReadFile(path)
		lines := strings.Split(string(b), "\n")
		host, _ := os.Hostname()
		if e == nil && len(lines) >= 3 && lines[2] == host {
			pid, e := strconv.Atoi(lines[0])
			if e == nil && pid > 0 && storageProcessGone(pid) {
				if e = os.Remove(path); e == nil {
					f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				}
			}
		}
	}
	if err != nil {
		return nil, errors.New("repository is locked; Qt/native operations must be quiesced")
	}
	host, _ := os.Hostname()
	executable, executableErr := os.Executable()
	if executableErr != nil {
		f.Close()
		os.Remove(path)
		return nil, executableErr
	}
	// QLockFile checks the live process executable name when detecting staleness.
	_, err = fmt.Fprintf(f, "%d\n%s\n%s\n", os.Getpid(), filepath.Base(executable), host)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	acquired = true
	return func() { os.Remove(path); releaseGate() }, nil
}
func (p *nativePublisher) target(path string) (string, error) {
	if path != "list.json" && path != "list_v2.json" && !strings.HasPrefix(path, "tracks/") {
		return "", ErrCensorConflict
	}
	if strings.HasPrefix(path, "tracks/") {
		id := strings.TrimPrefix(path, "tracks/")
		if len(id) == 0 || len(id) > 32 || strings.Trim(id, "0123456789") != "" {
			return "", ErrCensorConflict
		}
	}
	if filepath.Clean(path) != path || strings.Contains(path, "..") {
		return "", ErrCensorConflict
	}
	real, err := filepath.EvalSymlinks(p.repo)
	if err != nil || real != p.repo {
		return "", errors.New("repository root changed")
	}
	full := filepath.Join(p.repo, path)
	for _, name := range []string{p.repo, filepath.Dir(full), full} {
		st, e := os.Lstat(name)
		if errors.Is(e, os.ErrNotExist) && name == full {
			continue
		}
		if e != nil {
			return "", e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("repository target is a symlink")
		}
	}
	return full, nil
}
func (p *nativePublisher) read(path string) ([]byte, bool, error) {
	full, e := p.target(path)
	if e != nil {
		return nil, false, e
	}
	st, e := os.Stat(full)
	if errors.Is(e, os.ErrNotExist) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	limit := int64(512 << 20)
	if !strings.HasPrefix(path, "tracks/") {
		limit = 8 << 20
	}
	if !st.Mode().IsRegular() || st.Size() > limit {
		return nil, false, errors.New("invalid repository file")
	}
	f, e := os.Open(full)
	if e != nil {
		return nil, false, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, false, errors.New("repository file grew beyond limit")
	}
	return b, true, e
}
func (p *nativePublisher) saveJournal(path string, j nativeJournal) error {
	b, e := json.Marshal(j)
	if e != nil {
		return e
	}
	return nativeAtomic(path, b, 0600)
}

// apply preflights all CAS checks before writing. A prepared journal rolls forward
// after a crash; unexpected external edits block recovery rather than clobber them.
func (p *nativePublisher) apply(path string, j *nativeJournal) error {
	for _, c := range j.Changes {
		b, exists, e := p.read(c.Path)
		if e != nil {
			return e
		}
		d := nativeDigest(b)
		if !(exists == !c.Delete && d == c.After) && !(exists == c.Existed && d == c.Before) {
			return ErrCensorConflict
		}
	}
	for _, c := range j.Changes {
		b, exists, e := p.read(c.Path)
		if e != nil {
			return e
		}
		if exists == !c.Delete && nativeDigest(b) == c.After {
			continue
		}
		if c.Delete {
			full, e := p.target(c.Path)
			if e != nil {
				return e
			}
			if e = os.Remove(full); e != nil {
				return e
			}
			if e = nativeSync(filepath.Dir(full)); e != nil {
				return e
			}
			continue
		}
		after, e := os.ReadFile(filepath.Join(p.history, c.After))
		if e != nil || nativeDigest(after) != c.After {
			return errors.New("invalid private transaction blob")
		}
		full, e := p.target(c.Path)
		if e != nil {
			return e
		}
		if e = nativeAtomic(full, after, 0644); e != nil {
			return e
		}
	}
	j.Done = true
	return p.saveJournal(path, *j)
}
func (p *nativePublisher) recover() error {
	entries, e := os.ReadDir(p.history)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(p.history, entry.Name())
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var j nativeJournal
		if json.Unmarshal(b, &j) != nil {
			return errors.New("invalid publication journal")
		}
		if !j.Done {
			if e = p.apply(path, &j); e != nil {
				return e
			}
		}
	}
	return nil
}
func (p *nativePublisher) Recover(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	unlock, e := p.lock()
	if e != nil {
		return e
	}
	defer unlock()
	return p.recover()
}
func (p *nativePublisher) ReplacementReceipt(ctx context.Context, i CensorIdentity, s CensorSelection) (NativePublicationReceipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i.UserID == "" {
		return NativePublicationReceipt{}, ErrCensorDenied
	}
	if !validCensorReceiptSelection(s) {
		return NativePublicationReceipt{}, ErrCensorConflict
	}
	if e := ctx.Err(); e != nil {
		return NativePublicationReceipt{}, e
	}
	unlock, e := p.lock()
	if e != nil {
		return NativePublicationReceipt{}, e
	}
	defer unlock()
	if e = p.recover(); e != nil {
		return NativePublicationReceipt{}, e
	}
	return p.receipt(i, s)
}
func (p *nativePublisher) receipt(i CensorIdentity, s CensorSelection) (NativePublicationReceipt, error) {
	b, e := os.ReadFile(filepath.Join(p.history, nativeScope(i, s)+".json"))
	if errors.Is(e, os.ErrNotExist) {
		return NativePublicationReceipt{}, ErrCensorNotFound
	}
	var j nativeJournal
	if e != nil {
		return j.Receipt, e
	}
	if json.Unmarshal(b, &j) != nil || j.Receipt.UserID != i.UserID || j.Receipt.Selection != s || !j.Done {
		return j.Receipt, ErrCensorConflict
	}
	return j.Receipt, nil
}

func nativeObject(b []byte) (map[string]any, error) {
	b = bytes.TrimPrefix(b, []byte{239, 187, 191})
	check := json.NewDecoder(bytes.NewReader(b))
	check.UseNumber()
	if err := nativeJSONValue(check, 0); err != nil {
		return nil, err
	}
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(b, []byte{239, 187, 191})))
	d.UseNumber()
	e := d.Decode(&m)
	if e == nil {
		var extra any
		if d.Decode(&extra) != io.EOF {
			e = errors.New("trailing JSON")
		}
	}
	if m == nil && e == nil {
		e = errors.New("JSON object required")
	}
	return m, e
}

// Duplicate keys are ambiguous CAS inputs and must not be silently collapsed.
func nativeJSONValue(d *json.Decoder, depth int) error {
	if depth > 128 {
		return errors.New("repository JSON nesting exceeds limit")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid repository JSON key")
			}
			seen[name] = true
			if err := nativeJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := nativeJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid repository JSON delimiter")
	}
	_, err = d.Token()
	return err
}

// Mapping obtains approval-time CAS evidence, under the same repository lock.
// Main stores this value with the approval; it must not refresh it on a CAS retry.
func (p *nativePublisher) Mapping(ctx context.Context, trackID int64) (string, error) {
	if trackID <= 0 {
		return "", ErrCensorConflict
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	unlock, err := p.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	if err = p.recover(); err != nil {
		return "", err
	}
	legacy, exists, err := p.read("list.json")
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrCensorNotFound
	}
	v2, exists, err := p.read("list_v2.json")
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrCensorNotFound
	}
	doc, err := nativeObject(legacy)
	if err != nil {
		return "", err
	}
	tracks, ok := doc["tracks"].(map[string]any)
	if !ok {
		return "", ErrCensorConflict
	}
	id := strconv.FormatInt(trackID, 10)
	expected := ""
	if value, exists := tracks[id]; exists {
		expected, ok = value.(string)
		if !ok {
			return "", ErrCensorConflict
		}
		if _, err = nativeURL("", expected); err != nil {
			return "", err
		}
	}
	if _, _, err = nativeLists(legacy, v2, id, expected, "https://validation.invalid/"+id); err != nil {
		return "", err
	}
	return expected, nil
}

func nativeID(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	case map[string]any:
		return nativeID(x["id"])
	}
	return ""
}
func nativeURL(template string, v any) (string, error) {
	if m, ok := v.(map[string]any); ok {
		v = m["url"]
	}
	s := nativeID(v)
	if s == "" {
		return "", ErrCensorConflict
	}
	if template != "" {
		s = strings.ReplaceAll(template, "%%", s)
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
	}
	u, e := url.Parse(s)
	if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ErrCensorConflict
	}
	return s, nil
}

// Metadata source.tracks is not an audio mapping. Only tracksStorages is edited.
func nativeLists(legacy, v2 []byte, id, old, published string) ([]byte, []byte, error) {
	l, e := nativeObject(legacy)
	if e != nil {
		return nil, nil, e
	}
	tracks, ok := l["tracks"].(map[string]any)
	if !ok {
		return nil, nil, ErrCensorConflict
	}
	existing, exists := tracks[id]
	if exists && existing != old {
		return nil, nil, ErrCensorConflict
	}
	if !exists && old != "" {
		return nil, nil, ErrCensorConflict
	}
	v, e := nativeObject(v2)
	if e != nil {
		return nil, nil, e
	}
	sources, ok := v["sources"].([]any)
	if !ok {
		return nil, nil, ErrCensorConflict
	}
	found := false
	for _, raw := range sources {
		source, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, ErrCensorConflict
		}
		rawStores, has := source["tracksStorages"]
		if !has {
			continue
		}
		stores, ok := rawStores.([]any)
		if !ok {
			return nil, nil, ErrCensorConflict
		}
		updatedStores := make([]any, 0, len(stores))
		for _, rawStore := range stores {
			sourceFound := false
			replacement := map[string]any{"url": published}
			preserve := func(entry any) {
				sourceFound = true
				if metadata, ok := entry.(map[string]any); ok {
					for key, value := range metadata {
						if key != "id" && key != "url" && key != "durationMs" {
							replacement[key] = value
						}
					}
				}
			}
			store, ok := rawStore.(map[string]any)
			if !ok {
				return nil, nil, ErrCensorConflict
			}
			template := ""
			if t, has := store["urlTemplate"]; has {
				template, ok = t.(string)
				if !ok || !strings.Contains(template, "%%") {
					return nil, nil, ErrCensorConflict
				}
			}
			if rawIDs, has := store["trackIds"]; has {
				ids, ok := rawIDs.([]any)
				if !ok || template == "" {
					return nil, nil, ErrCensorConflict
				}
				keep := []any{}
				for _, entry := range ids {
					if nativeID(entry) == id {
						resolved, e := nativeURL(template, json.Number(id))
						if e != nil || resolved != old {
							return nil, nil, ErrCensorConflict
						}
						found = true
						preserve(entry)
					} else {
						keep = append(keep, entry)
					}
				}
				store["trackIds"] = keep
			}
			if rawMap, has := store["tracks"]; has {
				mapping, ok := rawMap.(map[string]any)
				if !ok {
					return nil, nil, ErrCensorConflict
				}
				if entry, has := mapping[id]; has {
					resolved, e := nativeURL(template, entry)
					if e != nil || resolved != old {
						return nil, nil, ErrCensorConflict
					}
					found = true
					preserve(entry)
					delete(mapping, id)
				}
			}
			updatedStores = append(updatedStores, store)
			if sourceFound {
				updatedStores = append(updatedStores, map[string]any{"tracks": map[string]any{id: replacement}})
			}
		}
		source["tracksStorages"] = updatedStores
	}
	if exists && !found {
		return nil, nil, ErrCensorConflict
	}
	if !exists && found {
		return nil, nil, ErrCensorConflict
	}
	// Existing replacements stay within their original version gates. New tracks
	// get a separate catch-all source; metadata sources are never repurposed.
	if !found {
		sources = append(sources, map[string]any{"supportedVersions": ">0.0.0", "tracksStorages": []any{map[string]any{"tracks": map[string]any{id: published}}}})
	}
	v["sources"] = sources
	tracks[id] = published
	a, e := json.MarshalIndent(l, "", "    ")
	if e != nil {
		return nil, nil, e
	}
	b, e := json.MarshalIndent(v, "", "    ")
	return append(a, '\n'), append(b, '\n'), e
}

// Replace takes independently refreshed source and candidate evidence plus the
// approval-time mapping from Mapping. Empty ExpectedMapping means absent.
func (p *nativePublisher) Replace(ctx context.Context, i CensorIdentity, s CensorSelection, r NativePublicationRequest, c CensorCandidate) (NativePublicationReceipt, error) {
	var zero NativePublicationReceipt
	if i.UserID == "" {
		return zero, ErrCensorDenied
	}
	// Web discovery has no trusted completeness or redistribution evidence.
	// Reject even persisted candidates from older discovery code that set FullAudio.
	if strings.HasPrefix(c.ID, webCandidatePrefix) {
		return zero, ErrCensorConflict
	}
	if !validCensorReceiptSelection(s) || r.ID != s.RequestID || r.TrackID != s.TrackID || c.ID != s.CandidateID || !c.Downloadable || !c.FullAudio {
		return zero, ErrCensorConflict
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	unlock, e := p.lock()
	if e != nil {
		return zero, e
	}
	defer unlock()
	if e = p.recover(); e != nil {
		return zero, e
	}
	if receipt, e := p.receipt(i, s); e == nil {
		return receipt, nil
	} else if !errors.Is(e, ErrCensorNotFound) {
		return zero, e
	}
	if e = ctx.Err(); e != nil {
		return zero, e
	}
	id := strconv.FormatInt(s.TrackID, 10)
	published := "https://raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/" + id
	paths := []string{"tracks/" + id, "list.json", "list_v2.json"}
	before := make([][]byte, 3)
	existed := make([]bool, 3)
	for n, path := range paths {
		before[n], existed[n], e = p.read(path)
		if e != nil {
			return zero, e
		}
	}
	if !existed[1] || !existed[2] {
		return zero, errors.New("both repository lists must exist")
	}
	a, b, e := nativeLists(before[1], before[2], id, r.ExpectedMapping, published)
	if e != nil {
		return zero, e
	}
	if _, e = nativeURL("", c.URL); e != nil {
		return zero, e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if e != nil {
		return zero, errors.New("invalid candidate URL")
	}
	client := *p.client

	if strings.HasPrefix(c.ID, archiveCandidatePrefix) {
		item, file, err := archiveDecodeID(c.ID)
		if err != nil || c.URL != archiveDownloadURL(item, file) || archiveLicense(c.Metadata["license_url"]) == "" || c.Provider != "internet_archive" {
			return zero, ErrCensorConflict
		}
		client.CheckRedirect = archiveRedirect
		if client.Transport == nil {
			client.Transport = archivePublicClient.Transport
		}
	}
	res, e := client.Do(req)
	if e != nil {
		return zero, errors.New("candidate download failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.ContentLength > 512<<20 {
		return zero, errors.New("candidate download is not a bounded full response")
	}
	f, e := os.CreateTemp(p.history, "download-*")
	if e != nil {
		return zero, e
	}
	defer os.Remove(f.Name())
	size, e := io.Copy(f, io.LimitReader(res.Body, (512<<20)+1))
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil || size == 0 || size > 512<<20 {
		return zero, errors.New("incomplete or oversized MP3 download")
	}
	if e = p.validate(ctx, f.Name()); e != nil {
		return zero, e
	}
	audio, e := os.ReadFile(f.Name())
	if e != nil {
		return zero, e
	}
	after := [][]byte{audio, a, b}
	j := nativeJournal{Receipt: NativePublicationReceipt{Selection: s, UserID: i.UserID, Status: "local_only", LocalPath: paths[0], PublicationURL: published, Durable: true}}
	if strings.HasPrefix(c.ID, archiveCandidatePrefix) || strings.HasPrefix(c.ID, webCandidatePrefix) {
		j.Provenance = c.Metadata
	}
	for n, path := range paths {
		current, exists, e := p.read(path)
		if e != nil {
			return zero, e
		}
		if exists != existed[n] || !bytes.Equal(current, before[n]) {
			return zero, ErrCensorConflict
		}
		for _, blob := range [][]byte{before[n], after[n]} {
			if e = nativeAtomic(filepath.Join(p.history, nativeDigest(blob)), blob, 0600); e != nil {
				return zero, e
			}
		}
		j.Changes = append(j.Changes, nativeFileChange{Path: path, Before: nativeDigest(before[n]), After: nativeDigest(after[n]), Existed: existed[n]})
	}
	if e = ctx.Err(); e != nil {
		return zero, e
	}
	journal := filepath.Join(p.history, nativeScope(i, s)+".json")
	if e = p.saveJournal(journal, j); e != nil {
		return zero, e
	}
	if e = p.apply(journal, &j); e != nil {
		return zero, e
	}
	return j.Receipt, nil
}

// Rollback restores exact preimages only if all files still match this receipt.
// It is itself journaled; repeated rollback is idempotent and never touches Git.
func (p *nativePublisher) Rollback(ctx context.Context, i CensorIdentity, s CensorSelection) (NativePublicationReceipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var zero NativePublicationReceipt
	if i.UserID == "" {
		return zero, ErrCensorDenied
	}
	if !validCensorReceiptSelection(s) {
		return zero, ErrCensorConflict
	}
	if e := ctx.Err(); e != nil {
		return zero, e
	}
	unlock, e := p.lock()
	if e != nil {
		return zero, e
	}
	defer unlock()
	if e = p.recover(); e != nil {
		return zero, e
	}
	path := filepath.Join(p.history, nativeScope(i, s)+".json")
	b, e := os.ReadFile(path)
	if e != nil {
		return zero, e
	}
	var j nativeJournal
	if json.Unmarshal(b, &j) != nil || j.Receipt.UserID != i.UserID || j.Receipt.Selection != s {
		return zero, ErrCensorConflict
	}
	if j.RolledBack {
		return j.Receipt, nil
	}
	for _, c := range j.Changes {
		current, exists, e := p.read(c.Path)
		if e != nil {
			return zero, e
		}
		if !exists || nativeDigest(current) != c.After {
			return zero, ErrCensorConflict
		}
	}
	for n, c := range j.Changes {
		j.Changes[n] = nativeFileChange{Path: c.Path, Before: c.After, After: c.Before, Existed: true, Delete: !c.Existed}
	}
	j.Done = false
	j.RolledBack = true
	j.Receipt.Status = "local_rolled_back"
	if e = p.saveJournal(path, j); e != nil {
		return zero, e
	}
	if e = p.apply(path, &j); e != nil {
		return zero, e
	}
	return j.Receipt, nil
}
