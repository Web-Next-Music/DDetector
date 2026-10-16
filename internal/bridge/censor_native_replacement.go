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
	"sort"
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
	duration      func(context.Context, string) (int64, error)
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
	if err != nil || !info.IsDir() || !storagePrivate(runtime, info, false) {
		return nil, errors.New("runtime must be a private directory (0700)")
	}
	h := sha256.Sum256([]byte(repo))
	history := filepath.Join(runtime, "native-publication-"+hex.EncodeToString(h[:]))
	if err := os.MkdirAll(history, 0700); err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(history)
	st, statErr := os.Stat(history)
	if err != nil || statErr != nil || real != history || !st.IsDir() || !storagePrivate(history, st, false) {
		return nil, errors.New("history must be a private nonsymlink directory")
	}
	p := &nativePublisher{repo: repo, history: history, client: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	p.duration = nativeMP3Duration
	p.validate = func(ctx context.Context, path string) error {
		_, err := nativeMP3Duration(ctx, path)
		return err
	}
	return p, nil
}

func nativeMP3Duration(ctx context.Context, path string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-xerror", "-protocol_whitelist", "file,pipe", "-f", "mp3", "-i", path, "-map", "0:a:0", "-progress", "pipe:1", "-f", "null", "-")
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return 0, errors.New("downloaded full MP3 failed FFmpeg validation")
	}
	var micros int64
	for _, line := range strings.Split(string(out), "\n") {
		if value, ok := strings.CutPrefix(line, "out_time_us="); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && n > micros {
				micros = n
			}
		}
	}
	ms := micros / 1000
	if ms <= 0 {
		return 0, errors.New("downloaded MP3 has no positive audio duration")
	}
	return ms, nil
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
	v2, exists, err := p.read("list_v2.json")
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrCensorNotFound
	}
	return nativeMapping(v2, strconv.FormatInt(trackID, 10))
}

// nativeMapping rejects ambiguous mappings across version gates rather than
// approving a replacement using evidence from an unrelated legacy list.
func nativeMapping(data []byte, id string) (string, error) {
	doc, err := nativeObject(data)
	if err != nil {
		return "", err
	}
	sources, ok := doc["sources"].([]any)
	if !ok {
		return "", ErrCensorConflict
	}
	result := ""
	accept := func(value string, err error) error {
		if err != nil {
			return err
		}
		if result != "" && result != value {
			return ErrCensorConflict
		}
		result = value
		return nil
	}
	for _, raw := range sources {
		source, ok := raw.(map[string]any)
		if !ok {
			return "", ErrCensorConflict
		}
		rawStores, has := source["tracksStorages"]
		if !has {
			continue
		}
		stores, ok := rawStores.([]any)
		if !ok {
			return "", ErrCensorConflict
		}
		for _, raw := range stores {
			store, ok := raw.(map[string]any)
			if !ok {
				return "", ErrCensorConflict
			}
			template := ""
			if raw, has := store["urlTemplate"]; has {
				template, ok = raw.(string)
				if !ok || !strings.Contains(template, "%%") {
					return "", ErrCensorConflict
				}
			}
			if raw, has := store["trackIds"]; has {
				ids, ok := raw.([]any)
				if !ok || template == "" {
					return "", ErrCensorConflict
				}
				for _, entry := range ids {
					if nativeID(entry) == id {
						if err := accept(nativeURL(template, json.Number(id))); err != nil {
							return "", err
						}
					}
				}
			}
			if raw, has := store["tracks"]; has {
				tracks, ok := raw.(map[string]any)
				if !ok {
					return "", ErrCensorConflict
				}
				if entry, has := tracks[id]; has {
					if err := accept(nativeURL(template, entry)); err != nil {
						return "", err
					}
				}
			}
		}
	}
	return result, nil
}

// Patch only changed JSON values. Unchanged values retain their original bytes,
// including key order, numeric spelling, escaping, indentation and line endings.
func nativePatchJSON(data []byte, value any) ([]byte, error) {
	return nativePatchJSONLayout(data, value, "", "", "")
}

func nativePatchJSONLayout(data []byte, value any, base, indent, newline string) ([]byte, error) {
	bom := []byte{239, 187, 191}
	if bytes.HasPrefix(data, bom) {
		patched, err := nativePatchJSONLayout(data[len(bom):], value, base, indent, newline)
		return append(append([]byte{}, bom...), patched...), err
	}
	if newline == "" {
		newline = "\n"
		if bytes.Contains(data, []byte("\r\n")) {
			newline = "\r\n"
		} else if bytes.Contains(data, []byte("\r")) {
			newline = "\r"
		}
		if line := bytes.Index(data, []byte(newline)); line >= 0 {
			for p := line + len(newline); p < len(data) && (data[p] == ' ' || data[p] == '\t'); p++ {
				indent += string(data[p])
			}
		}
		if indent == "" {
			indent = "    "
		}
	}
	multiline := bytes.Contains(data, []byte(newline))
	serialize := func(v any, prefix string) ([]byte, error) {
		raw, err := json.Marshal(v)
		if err != nil || !multiline {
			return raw, err
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, prefix, indent); err != nil {
			return nil, err
		}
		return bytes.ReplaceAll(pretty.Bytes(), []byte("\n"), []byte(newline)), nil
	}
	compact, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var a, b bytes.Buffer
	if json.Compact(&a, data) == nil && json.Compact(&b, compact) == nil {
		var original any
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		if err := d.Decode(&original); err != nil {
			return nil, err
		}
		canonical, err := json.Marshal(original)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(canonical, compact) {
			return data, nil
		}
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, ErrCensorConflict
	}
	object, isObject := value.(map[string]any)
	array, isArray := value.([]any)
	if !(isObject && trimmed[0] == '{') && !(isArray && trimmed[0] == '[') {
		return compact, nil
	}
	type part struct {
		key               string
		start, value, end int
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	opening := int(d.InputOffset())
	parts := []part{}
	for d.More() {
		start := int(d.InputOffset())
		for start < len(data) && strings.ContainsRune(" \t\r\n,", rune(data[start])) {
			start++
		}
		p := part{start: start}
		if isObject {
			token, err := d.Token()
			if err != nil {
				return nil, err
			}
			p.key = token.(string)
		}
		p.value = int(d.InputOffset())
		if !isObject {
			p.value = start
		}
		for p.value < len(data) && strings.ContainsRune(" \t\r\n:", rune(data[p.value])) {
			p.value++
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return nil, err
		}
		p.end = int(d.InputOffset())
		parts = append(parts, p)
	}
	closing := int(d.InputOffset())
	for closing < len(data) && strings.ContainsRune(" \t\r\n", rune(data[closing])) {
		closing++
	}
	prefix, suffix, separator := "", "", ","
	if len(parts) > 0 {
		prefix = string(data[opening:parts[0].start])
		suffix = string(data[parts[len(parts)-1].end:closing])
		separator += prefix
		if len(parts) > 1 {
			separator = string(data[parts[0].end:parts[1].start])
		}
	} else {
		prefix = string(data[opening:closing])
	}
	entryIndent := base + indent
	if len(parts) > 0 {
		if line := bytes.LastIndex(data[:parts[0].start], []byte(newline)); line >= 0 {
			padding := string(data[line+len(newline) : parts[0].start])
			if strings.Trim(padding, " \t") == "" {
				entryIndent = padding
			}
		}
	} else if multiline {
		suffix = prefix
		prefix = newline + entryIndent
		separator = "," + prefix
	}
	entries := [][]byte{}
	seen := map[string]bool{}
	for n, p := range parts {
		var next any
		if isObject {
			var exists bool
			next, exists = object[p.key]
			if !exists {
				continue
			}
			seen[p.key] = true
		} else {
			if n >= len(array) {
				continue
			}
			next = array[n]
		}
		patched, err := nativePatchJSONLayout(data[p.value:p.end], next, entryIndent, indent, newline)
		if err != nil {
			return nil, err
		}
		entry := []byte{}
		if len(entries) > 0 {
			entry = append(entry, data[parts[n-1].end:p.start]...)
		}
		entry = append(entry, data[p.start:p.value]...)
		entries = append(entries, append(entry, patched...))
	}
	if isObject {
		// New keys are rare here; use the encoder's deterministic key ordering.

		keys := make([]string, 0, len(object))
		for key := range object {
			if !seen[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			name, _ := json.Marshal(key)
			raw, err := serialize(object[key], entryIndent)
			if err != nil {
				return nil, err
			}
			colon := ":"
			if multiline {
				colon = ": "
			}
			entry := []byte{}
			if len(entries) > 0 {
				entry = append(entry, separator...)
			}
			entry = append(entry, name...)
			entry = append(entry, colon...)
			entries = append(entries, append(entry, raw...))
		}
	} else {
		for n := len(parts); n < len(array); n++ {
			raw, err := serialize(array[n], entryIndent)
			if err != nil {
				return nil, err
			}
			if len(entries) > 0 {
				raw = append([]byte(separator), raw...)
			}
			entries = append(entries, raw)
		}
	}
	out := append([]byte{}, data[:opening]...)
	out = append(out, prefix...)
	out = append(out, bytes.Join(entries, nil)...)
	out = append(out, suffix...)
	return append(out, data[closing:]...), nil
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
func nativeLists(legacy, v2 []byte, id, old, published string, durationMs int64) ([]byte, []byte, error) {
	expected, e := nativeMapping(v2, id)
	if e != nil {
		return nil, nil, e
	}
	if expected != old {
		return nil, nil, ErrCensorConflict
	}
	if expected == published {
		return legacy, v2, nil
	}
	exists := expected != ""
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
		updatedStores := append([]any{}, stores...)
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
	if !found {
		var targetSource map[string]any
		for _, raw := range sources {
			source := raw.(map[string]any)
			if source["supportedVersions"] != ">=2.0.0" {
				continue
			}
			if targetSource != nil {
				return nil, nil, fmt.Errorf("ambiguous >=2.0.0 source: %w", ErrCensorConflict)
			}
			targetSource = source
		}
		if targetSource == nil {
			targetSource = map[string]any{"supportedVersions": ">=2.0.0"}
			sources = append(sources, targetSource)
		}
		var target map[string]any
		compatibleTemplate := ""
		for _, raw := range sources {
			matchedTemplate := false
			source := raw.(map[string]any)
			stores, _ := source["tracksStorages"].([]any)
			for _, rawStore := range stores {
				store := rawStore.(map[string]any)
				template, _ := store["urlTemplate"].(string)
				if template == "" {
					continue
				}
				resolved, err := nativeURL(template, id)
				if err != nil {
					return nil, nil, err
				}
				if resolved != published {
					continue
				}
				if compatibleTemplate != "" && compatibleTemplate != template {
					return nil, nil, fmt.Errorf("ambiguous compatible v2 templates: %w", ErrCensorConflict)
				}
				if matchedTemplate {
					return nil, nil, fmt.Errorf("ambiguous compatible v2 storages: %w", ErrCensorConflict)
				}
				matchedTemplate = true
				compatibleTemplate = template
				if source["supportedVersions"] != ">=2.0.0" {
					continue
				}
				if target != nil {
					return nil, nil, fmt.Errorf("ambiguous compatible v2 storages: %w", ErrCensorConflict)
				}
				target = store
			}
		}
		if target == nil {
			target = map[string]any{"tracks": map[string]any{}}
			if compatibleTemplate != "" {
				target = map[string]any{"urlTemplate": compatibleTemplate, "trackIds": []any{}}
			}
			stores, _ := targetSource["tracksStorages"].([]any)
			targetSource["tracksStorages"] = append(stores, target)
		}
		{
			if ids, has := target["trackIds"]; has {
				target["trackIds"] = append(ids.([]any), map[string]any{"id": json.Number(id), "durationMs": durationMs})
			} else {
				tracks, _ := target["tracks"].(map[string]any)
				if tracks == nil {
					return nil, nil, ErrCensorConflict
				}
				if _, templated := target["urlTemplate"]; templated {
					tracks[id] = id
				} else {
					tracks[id] = published
				}
			}
		}
	}
	v["sources"] = sources
	b, e := nativePatchJSON(v2, v)
	return legacy, b, e
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
	paths := []string{"tracks/" + id, "list_v2.json"}
	before := make([][]byte, len(paths))
	existed := make([]bool, len(paths))
	for n, path := range paths {
		before[n], existed[n], e = p.read(path)
		if e != nil {
			return zero, e
		}
	}
	if !existed[1] {
		return zero, errors.New("repository list_v2.json must exist")
	}
	_, b, e := nativeLists(nil, before[1], id, r.ExpectedMapping, published, 0)
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
	if r.ExpectedMapping == "" {
		durationMs, err := p.duration(ctx, f.Name())
		if err != nil {
			return zero, err
		}
		_, b, e = nativeLists(nil, before[1], id, r.ExpectedMapping, published, durationMs)
		if e != nil {
			return zero, e
		}
	} else if e = p.validate(ctx, f.Name()); e != nil {
		return zero, e
	}
	audio, e := os.ReadFile(f.Name())
	if e != nil {
		return zero, e
	}
	after := [][]byte{audio, b}
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
