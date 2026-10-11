package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const archiveCandidatePrefix = "candidate:archive:"
const archiveItemLimit = 12
const archiveCandidateLimit = 20
const archiveJSONLimit = 2 << 20
const archiveAudioLimit = 512 << 20

var archiveIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var archiveDataPath = regexp.MustCompile(`^/[0-9]+/items/`)

// The ID is self-contained across restarts; no caller URL or ephemeral registry
// is trusted. Long file names are omitted to respect the bridge's 256-byte IDs.
func archiveCandidateID(identifier, file string) string {
	if !archiveIdentifier.MatchString(identifier) || !archiveFileName(file) {
		return ""
	}
	id := archiveCandidatePrefix + base64.RawURLEncoding.EncodeToString([]byte(identifier)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(file))
	if !censorID(id) {
		return ""
	}
	return id
}
func archiveDecodeID(id string) (string, string, error) {
	if !censorID(id) || !strings.HasPrefix(id, archiveCandidatePrefix) {
		return "", "", ErrCensorNotFound
	}
	parts := strings.Split(strings.TrimPrefix(id, archiveCandidatePrefix), ":")
	if len(parts) != 2 {
		return "", "", ErrCensorNotFound
	}
	item, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	file, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	if e1 != nil || e2 != nil || archiveCandidateID(string(item), string(file)) != id {
		return "", "", ErrCensorNotFound
	}
	return string(item), string(file), nil
}
func archiveFileName(file string) bool {
	if file == "" || len(file) > 180 || !utf8.ValidString(file) || path.Clean(file) != file || strings.HasPrefix(file, "/") || strings.ContainsAny(file, "\\%?#") || strings.ToLower(path.Ext(file)) != ".mp3" {
		return false
	}
	for _, segment := range strings.Split(file, "/") {
		if segment == "." || segment == ".." || segment == "" {
			return false
		}
	}
	for _, r := range file {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func archiveDownloadURL(item, file string) string {
	segments := strings.Split(file, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	return "https://archive.org/download/" + url.PathEscape(item) + "/" + strings.Join(segments, "/")
}

// Archive redirects can point to its data nodes, never arbitrary hosts or HTTP.
func archiveMediaURL(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && u.User == nil && u.Opaque == "" && u.Fragment == "" && u.Port() == "" && (host == "archive.org" || strings.HasSuffix(host, ".archive.org")) && (strings.HasPrefix(u.Path, "/download/") || strings.HasPrefix(u.Path, "/items/") || archiveDataPath.MatchString(u.Path))
}
func archiveRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 || !archiveMediaURL(req.URL) {
		return errors.New("unsafe Internet Archive redirect")
	}
	return nil
}
func archivePublicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
func archiveTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("Internet Archive DNS returned no addresses")
		}
		for _, ip := range ips {
			if !archivePublicIP(ip.IP) {
				return nil, errors.New("Internet Archive private address rejected")
			}
		}
		var last error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}}
}

var archivePublicClient = &http.Client{Timeout: 15 * time.Second, Transport: archiveTransport(), CheckRedirect: archiveRedirect}

func (n *nativeSource) archiveClient() *http.Client {
	if n.archiveHTTP != nil {
		return n.archiveHTTP
	}
	return archivePublicClient
}
func (n *nativeSource) archiveJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://archive.org"+endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	client := *n.archiveClient()
	// Metadata/search never redirect; this also prevents following login pages.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrCensorUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Internet Archive API failed (HTTP %d)", res.StatusCode)
	}
	if res.ContentLength > archiveJSONLimit {
		return errors.New("Internet Archive JSON exceeds limit")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, archiveJSONLimit+1))
	if err != nil {
		return err
	}
	if len(data) > archiveJSONLimit {
		return errors.New("Internet Archive JSON exceeds limit")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("invalid Internet Archive JSON")
	}
	return nil
}

type archiveRecord map[string]json.RawMessage

func (r archiveRecord) texts(key string) []string {
	var s string
	if json.Unmarshal(r[key], &s) == nil {
		return []string{s}
	}
	var ss []string
	if json.Unmarshal(r[key], &ss) == nil {
		return ss
	}
	return nil
}
func (r archiveRecord) text(key string) string { return strings.Join(r.texts(key), "; ") }
func (r archiveRecord) flagged(keys ...string) bool {
	for _, key := range keys {
		raw, ok := r[key]
		if !ok || len(raw) == 0 {
			continue
		}
		// Unknown/nonempty flag representations fail closed.
		v := strings.ToLower(strings.TrimSpace(string(raw)))
		if v != "false" && v != "0" && v != "null" && v != `"false"` && v != `"0"` && v != `""` && v != "[]" {
			return true
		}
	}
	return false
}

type archiveItem struct {
	Metadata   archiveRecord   `json:"metadata"`
	Files      []archiveRecord `json:"files"`
	Restricted json.RawMessage `json:"is_dark"`
	Error      json.RawMessage `json:"error"`
}

// Deliberately excludes NC, ND, SA and ambiguous prose. Rights claims are
// uploader-provided evidence, not independent legal verification.
func archiveLicense(value string) string {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host != "creativecommons.org" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	p := strings.TrimSuffix(u.Path, "/")
	if p == "/publicdomain/zero/1.0" || p == "/publicdomain/mark/1.0" {
		return "https://creativecommons.org" + p + "/"
	}
	for _, version := range []string{"1.0", "2.0", "2.5", "3.0", "4.0"} {
		if p == "/licenses/by/"+version {
			return "https://creativecommons.org" + p + "/"
		}
	}
	return ""
}
func archiveItemLicense(item archiveItem, identifier string) string {
	m := item.Metadata
	if (archiveRecord{"error": item.Error}).flagged("error") || (archiveRecord{"dark": item.Restricted}).flagged("dark") || m.text("identifier") != identifier || m.text("mediatype") != "audio" || m.flagged("access-restricted-item", "no-download", "nodownload", "is_dark", "dark", "private", "drm", "preview", "borrowable", "is_borrowable", "is_lendable") {
		return ""
	}
	for key, raw := range m {
		if strings.HasPrefix(key, "lending") && (archiveRecord{key: raw}).flagged(key) {
			return ""
		}
	}
	for _, collection := range m.texts("collection") {
		lower := strings.ToLower(collection)
		if strings.Contains(lower, "restricted") || strings.Contains(lower, "lending") || strings.Contains(lower, "borrow") || lower == "stream_only" {
			return ""
		}
	}
	licenses := m.texts("licenseurl")
	if len(licenses) != 1 {
		return ""
	}
	return archiveLicense(licenses[0])
}
func archivePreview(value string) bool {
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		switch token {
		case "preview", "sample", "snippet", "excerpt", "trailer", "drm":
			return true
		}
	}
	return false
}
func archiveNumber(r archiveRecord, key string) float64 {
	text := r.text(key)
	if text == "" {
		text = string(r[key])
	}
	value, _ := strconv.ParseFloat(text, 64)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}
func archiveDuration(file archiveRecord) float64 {
	value := file.text("length")
	if !strings.Contains(value, ":") {
		return archiveNumber(file, "length")
	}
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	seconds := 0.0
	for index, part := range parts {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) || (index > 0 && n >= 60) {
			return 0
		}
		seconds = seconds*60 + n
	}
	return seconds
}

func archiveCandidate(item archiveItem, identifier string, file archiveRecord) (CensorCandidate, bool) {
	license := archiveItemLicense(item, identifier)
	name := file.text("name")
	id := archiveCandidateID(identifier, name)
	size := archiveNumber(file, "size")
	duration := archiveDuration(file)
	title := file.text("title")
	if title == "" {
		title = item.Metadata.text("title")
	}
	artist := file.text("artist")
	if artist == "" {
		artist = item.Metadata.text("creator")
	}
	if license == "" || id == "" || file.text("source") != "original" || size <= 0 || math.Trunc(size) != size || size > archiveAudioLimit || duration <= 0 || duration > 86400 || file.flagged("private", "restricted", "access-restricted-item", "drm", "preview", "no-download", "nodownload", "borrowable") || archivePreview(name+" "+title+" "+item.Metadata.text("title")) {
		return CensorCandidate{}, false
	}
	if override := file.text("licenseurl"); override != "" && archiveLicense(override) != license {
		return CensorCandidate{}, false
	}
	c := CensorCandidate{ID: id, Title: title, Artist: artist, Provider: "internet_archive", URL: archiveDownloadURL(identifier, name), DurationSeconds: duration, FullAudio: true, Downloadable: true, Metadata: map[string]string{
		"kind": "audio", "content_type": "audio/mpeg", "format": "mp3", "archive_identifier": identifier, "archive_file": name, "source_url": "https://archive.org/details/" + identifier, "metadata_url": "https://archive.org/metadata/" + identifier, "license_url": license, "attribution_title": title, "attribution_creator": artist, "rights": item.Metadata.text("rights"), "file_source": "original", "size": strconv.FormatInt(int64(size), 10), "uncensored": "unverified",
	}}
	if validateCensorCandidates([]CensorCandidate{c}) != nil {
		return CensorCandidate{}, false
	}
	return c, true
}
func (n *nativeSource) archiveDownloadable(ctx context.Context, c CensorCandidate) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.URL, nil)
	if err != nil || !archiveMediaURL(req.URL) {
		return ErrCensorInvalid
	}
	client := *n.archiveClient()
	client.CheckRedirect = archiveRedirect
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrCensorUnavailable
	}
	defer res.Body.Close()
	contentType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	size, _ := strconv.ParseInt(c.Metadata["size"], 10, 64)
	if res.StatusCode != http.StatusOK || contentType != "audio/mpeg" || res.ContentLength != size || res.ContentLength <= 0 || res.ContentLength > archiveAudioLimit {
		return errors.New("Internet Archive file is not a public full MP3 download")
	}
	return nil
}
func (n *nativeSource) archiveResolve(ctx context.Context, id string) (CensorCandidate, error) {
	identifier, name, err := archiveDecodeID(id)
	if err != nil {
		return CensorCandidate{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var item archiveItem
	if err := n.archiveJSON(ctx, "/metadata/"+url.PathEscape(identifier), &item); err != nil {
		return CensorCandidate{}, err
	}
	if len(item.Files) > 1000 {
		return CensorCandidate{}, ErrCensorUnavailable
	}
	for _, file := range item.Files {
		if file.text("name") != name {
			continue
		}
		c, ok := archiveCandidate(item, identifier, file)
		if !ok {
			return CensorCandidate{}, ErrCensorConflict
		}
		if err := n.archiveDownloadable(ctx, c); err != nil {
			return CensorCandidate{}, err
		}
		return c, nil
	}
	return CensorCandidate{}, ErrCensorNotFound
}
func (n *nativeSource) archiveSearch(ctx context.Context, query string) ([]CensorCandidate, error) {
	query = strings.TrimSpace(query)
	if !validCensorQuery(query) {
		return nil, errors.New("audio search requires a query of at most 1024 bytes")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Each term is a quoted literal, not caller-supplied Lucene syntax. This
	// searches Archive metadata only, not the web or an asserted explicit catalog.
	terms := []string{}
	for _, term := range strings.Fields(query) {
		terms = append(terms, `"`+strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(term)+`"`)
	}
	values := url.Values{"q": {"mediatype:audio AND (" + strings.Join(terms, " AND ") + ")"}, "fl[]": {"identifier"}, "rows": {strconv.Itoa(archiveItemLimit)}, "page": {"1"}, "output": {"json"}}
	var result struct {
		Response *struct {
			Docs []struct {
				Identifier string `json:"identifier"`
			} `json:"docs"`
		} `json:"response"`
		Error json.RawMessage `json:"error"`
	}
	if err := n.archiveJSON(ctx, "/advancedsearch.php?"+values.Encode(), &result); err != nil {
		return nil, err
	}
	if (archiveRecord{"error": result.Error}).flagged("error") || result.Response == nil || len(result.Response.Docs) > archiveItemLimit {
		return nil, errors.New("invalid bounded Internet Archive search response")
	}
	out := []CensorCandidate{}
	seenItems, seenCandidates := map[string]bool{}, map[string]bool{}
	for _, doc := range result.Response.Docs {
		identifier := doc.Identifier
		if !archiveIdentifier.MatchString(identifier) || seenItems[identifier] {
			continue
		}
		seenItems[identifier] = true
		var item archiveItem
		if err := n.archiveJSON(ctx, "/metadata/"+url.PathEscape(identifier), &item); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if len(item.Files) > 1000 || archiveItemLicense(item, identifier) == "" {
			continue
		}
		for _, file := range item.Files {
			c, ok := archiveCandidate(item, identifier, file)
			if !ok || seenCandidates[c.ID] {
				continue
			}
			seenCandidates[c.ID] = true
			// Bound probes as well as returned candidates, even if every probe fails.
			if len(seenCandidates) > archiveCandidateLimit {
				return out, nil
			}
			if err := n.archiveDownloadable(ctx, c); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
			out = append(out, c)
		}
	}
	return out, nil
}
