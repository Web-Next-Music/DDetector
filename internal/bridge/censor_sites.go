package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sync"

	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const webCandidatePrefix = "candidate:web:"
const webCandidatesPerPage = 1

// Only public HTTPS URLs are accepted; the transport separately checks every
// DNS answer and dials the checked address (without proxies or a second lookup).
func webURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.Contains(host, "%") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return archivePublicIP(ip)
	}
	return strings.Contains(host, ".")
}
func webRedirect(r *http.Request, via []*http.Request) error {
	if len(via) >= 5 || !webURL(r.URL) {
		return errors.New("unsafe web discovery redirect")
	}
	return nil
}

var webPublicClient = &http.Client{Timeout: 15 * time.Second, Transport: archiveTransport(), CheckRedirect: webRedirect}

func webID(source, audio string) string {
	u, err := url.Parse(source)
	if err != nil || !webURL(u) {
		return ""
	}
	sum := sha256.Sum256([]byte(audio))
	id := webCandidatePrefix + base64.RawURLEncoding.EncodeToString([]byte(source)) + ":" + hex.EncodeToString(sum[:16])
	if !censorID(id) {
		return ""
	}
	return id
}
func webDecode(id string) (string, error) {
	if !censorID(id) || !strings.HasPrefix(id, webCandidatePrefix) {
		return "", ErrCensorNotFound
	}
	parts := strings.Split(strings.TrimPrefix(id, webCandidatePrefix), ":")
	if len(parts) != 2 || len(parts[1]) != 32 {
		return "", ErrCensorNotFound
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrCensorNotFound
	}
	if _, err = hex.DecodeString(parts[1]); err != nil {
		return "", ErrCensorNotFound
	}
	u, err := url.Parse(string(b))
	if err != nil || !webURL(u) {
		return "", ErrCensorNotFound
	}
	return string(b), nil
}
func (n *nativeSource) webClient() *http.Client {
	if n.webHTTP != nil {
		return n.webHTTP
	}
	return webPublicClient
}
func boundedBody(res *http.Response, limit int64) ([]byte, error) {
	if res.ContentLength > limit {
		return nil, errors.New("web response exceeds limit")
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("web response exceeds limit")
	}
	return b, nil
}
func (n *nativeSource) webRequest(ctx context.Context, method, raw string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, raw, nil)
	if err != nil || !webURL(req.URL) {
		return nil, ErrCensorInvalid
	}
	client := *n.webClient()
	client.CheckRedirect = webRedirect
	return client.Do(req)
}

// Static HTML only: no scripts, frames, playlists, DRM, cookies, or browser
// execution. Links come from audio sources, MP3 hrefs, download-labelled
// anchors in any language, download attributes, og:audio and JSON-LD.
var webTags = regexp.MustCompile(`(?is)<(audio|source)\b[^>]{0,1000}>`)
var webAnchors = regexp.MustCompile(`(?is)<a\b([^>]{0,1000})>(.{0,400}?)</a>`)
var webMetas = regexp.MustCompile(`(?is)<meta\b[^>]{0,1000}>`)
var webAttrs = regexp.MustCompile(`(?is)\b([a-z][a-z0-9:_-]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
var webDataURLs = regexp.MustCompile(`(?i)(?:"contentUrl"|"audioUrl"|"downloadUrl"|"streamUrl"|"download"|"play"|"mp3"|"file")\s*:\s*"(https?:[^"\\]+)"`)
var webTitle = regexp.MustCompile(`(?is)<(title|h1)\b[^>]*>(.{0,300}?)</(?:title|h1)>`)
var webTagStrip = regexp.MustCompile(`(?s)<[^>]*>`)
var webDownloadWord = regexp.MustCompile(`(?i)download|скачать|скачай|загрузить|завантажити|descargar|télécharger|telecharger|herunterladen|baixar|scarica|indir|pobierz|stáhnout|letöltés|ladda ner|downloaden|下载|下載|ダウンロード|다운로드|تحميل|डाउनलोड|ดาวน์โหลด|tải xuống|unduh|mp3`)

func webAttrMap(tag string) map[string]string {
	m := map[string]string{}
	for _, a := range webAttrs.FindAllStringSubmatch(tag, 40) {
		v := a[2]
		if v == "" {
			v = a[3]
		}
		m[strings.ToLower(a[1])] = html.UnescapeString(v)
	}
	return m
}

func webPageText(body string) string {
	parts := []string{}
	for _, t := range webTitle.FindAllStringSubmatch(body, 6) {
		parts = append(parts, strings.TrimSpace(html.UnescapeString(webTagStrip.ReplaceAllString(t[2], " "))))
	}
	for _, m := range webMetas.FindAllString(body, 60) {
		attrs := webAttrMap(m)
		key := strings.ToLower(attrs["property"] + attrs["name"])
		if key == "og:title" || key == "twitter:title" || key == "og:audio:title" || key == "music:song" {
			parts = append(parts, attrs["content"])
		}
	}
	text := strings.Join(strings.Fields(strings.Join(parts, " | ")), " ")
	if len(text) > 400 {
		text = text[:400]
	}
	return text
}

func webLinks(base *url.URL, body string) []string {
	var links []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(strings.ToLower(raw), "javascript:") {
			return
		}
		u, err := url.Parse(raw)
		if err != nil {
			return
		}
		u = base.ResolveReference(u)
		if webURL(u) {
			links = append(links, u.String())
		}
	}
	for _, tag := range webTags.FindAllString(body, 100) {
		add(webAttrMap(tag)["src"])
	}
	for _, m := range webMetas.FindAllString(body, 60) {
		attrs := webAttrMap(m)
		switch strings.ToLower(attrs["property"] + attrs["name"]) {
		case "og:audio", "og:audio:url", "og:audio:secure_url", "twitter:player:stream":
			add(attrs["content"])
		}
	}
	for _, a := range webAnchors.FindAllStringSubmatch(body, 400) {
		attrs := webAttrMap(a[1])
		label := strings.Join([]string{webTagStrip.ReplaceAllString(a[2], " "), attrs["title"], attrs["aria-label"], attrs["class"], attrs["id"], attrs["download"]}, " ")
		_, hasDownload := attrs["download"]
		href := attrs["href"]
		if href == "" {
			href = attrs["data-href"]
		}
		if href == "" {
			href = attrs["data-url"]
		}
		if u, err := url.Parse(href); err == nil && strings.EqualFold(path.Ext(u.Path), ".mp3") || hasDownload || webDownloadWord.MatchString(html.UnescapeString(label)) {
			add(href)
		}
		for _, k := range []string{"data-mp3", "data-src", "data-url", "data-audio", "data-file"} {
			if u, err := url.Parse(attrs[k]); err == nil && strings.EqualFold(path.Ext(u.Path), ".mp3") {
				add(attrs[k])
			}
		}
	}
	// React/server-component payloads often contain a JSON string nested inside
	// another script string, so quotes and slashes are escaped one extra time.
	// Normalize only these delimiters before extracting explicitly audio-related
	// URL fields; every resulting URL still passes webURL and an MP3 MIME probe.
	dataBody := strings.ReplaceAll(strings.ReplaceAll(body, `\"`, `"`), `\/`, "/")
	for _, m := range webDataURLs.FindAllStringSubmatch(dataBody, 50) {
		add(m[1])
	}
	return links
}

func (n *nativeSource) webProbe(ctx context.Context, audio string) (string, int64, bool) {
	res, err := n.webRequest(ctx, http.MethodHead, audio)
	if err != nil && ctx.Err() != nil {
		return "", 0, false
	}
	if err != nil || res.StatusCode != 200 {
		if res != nil {
			res.Body.Close()
		}
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, audio, nil)
		if rerr != nil || !webURL(req.URL) {
			return "", 0, false
		}
		req.Header.Set("Range", "bytes=0-0")
		client := *n.webClient()
		client.CheckRedirect = webRedirect
		res, err = client.Do(req)
		if err != nil {
			return "", 0, false
		}
		defer res.Body.Close()
		size := res.ContentLength
		if res.StatusCode == http.StatusPartialContent {
			_, total, ok := strings.Cut(res.Header.Get("Content-Range"), "/")
			if !ok {
				return "", 0, false
			}
			size, err = strconv.ParseInt(total, 10, 64)
			if err != nil {
				return "", 0, false
			}
		} else if res.StatusCode != 200 {
			return "", 0, false
		}
		return webAudioType(res), size, webAudioType(res) != "" && size > 0 && size <= archiveAudioLimit
	}
	defer res.Body.Close()
	ct := webAudioType(res)
	return ct, res.ContentLength, ct != "" && res.ContentLength > 0 && res.ContentLength <= archiveAudioLimit
}

func webAudioType(res *http.Response) string {
	ct, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	switch ct {
	case "audio/mpeg", "audio/mp3":
		return "audio/mpeg"
	case "application/octet-stream", "binary/octet-stream", "application/force-download":
		_, params, _ := mime.ParseMediaType(res.Header.Get("Content-Disposition"))
		if strings.EqualFold(path.Ext(params["filename"]), ".mp3") || strings.EqualFold(path.Ext(res.Request.URL.Path), ".mp3") {
			return "audio/mpeg"
		}
	}
	return ""
}

func (n *nativeSource) webDiscover(ctx context.Context, source string) ([]CensorCandidate, error) {
	res, err := n.webRequest(ctx, http.MethodGet, source)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("web discovery page unavailable")
	}
	kind, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	links := []string{}
	pageText := ""
	if kind == "audio/mpeg" {
		links = append(links, source)
	} else if kind == "text/html" {
		b, err := boundedBody(res, 2<<20)
		if err != nil {
			return nil, err
		}
		body := string(b)
		pageText = webPageText(body)
		links = webLinks(res.Request.URL, body)
		if audio, title, ok := hitmosTrackAudio(res.Request.URL, body); ok {
			links, pageText = audio, title
		}
	} else {
		return nil, errors.New("unsupported web discovery content type")
	}
	out := []CensorCandidate{}
	seen := map[string]bool{}
	for _, audio := range links {
		if seen[audio] {
			continue
		}
		seen[audio] = true
		if len(seen) > 20 {
			break
		}
		id := webID(source, audio)
		if id == "" || archivePreview(audio) {
			continue
		}
		if _, _, ok := n.webProbe(ctx, audio); !ok {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		title := pageText
		if title == "" {
			if u, err := url.Parse(audio); err == nil {
				title = path.Base(u.Path)
			}
		}
		// MIME and size establish downloadable media, not full-track completeness
		// or redistribution permission. Keep approval eligibility conservative.
		site := ""
		if u, err := url.Parse(source); err == nil {
			site = strings.ToLower(u.Hostname())
		}
		c := CensorCandidate{ID: id, Title: title, Provider: "direct_web", URL: audio, Downloadable: true, FullAudio: false, Metadata: map[string]string{"kind": "audio", "format": "mp3", "content_type": "audio/mpeg", "source_url": source, "audio_url": audio, "page_title": pageText, "rights": "unverified", "uncensored": "unverified", "completeness": "unverified", "discovery_provider": "direct_web", "site": site}}
		if validateCensorCandidates([]CensorCandidate{c}) == nil {
			out = append(out, c)
			// A track page may embed a long recommendation carousel. Its first
			// links describe the requested track; bounding each source keeps one
			// site from crowding candidates discovered on other result pages.
			if len(out) >= webCandidatesPerPage {
				break
			}
		}
	}
	return out, nil
}
func (n *nativeSource) webResolve(ctx context.Context, id string) (CensorCandidate, error) {
	source, err := webDecode(id)
	if err != nil {
		return CensorCandidate{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	candidates, err := n.webDiscover(ctx, source)
	if err != nil {
		return CensorCandidate{}, err
	}
	for _, c := range candidates {
		if c.ID == id {
			return c, nil
		}
	}
	return CensorCandidate{}, ErrCensorConflict
}

var hitmosTrackStart = regexp.MustCompile(`\{"id":\s*[0-9]+,\s*"title":`)
var hitmosFlightData = regexp.MustCompile(`self\.__next_f\.push\(\[1,("(?:[^"\\]|\\.)*")\]\)`)

// Read only the requested track, not recommendations. The playback URL is a
// public MP3 fallback when the separately hosted download is unavailable.
func hitmosTrackAudio(page *url.URL, body string) ([]string, string, bool) {
	host := strings.TrimPrefix(strings.ToLower(page.Hostname()), "www.")
	if host != "hitmos.fm" && host != "rus.hitmos.fm" {
		return nil, "", false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(page.Path, "/song/"), 10, 64)
	if err != nil || !strings.HasPrefix(page.Path, "/song/") {
		return nil, "", false
	}
	data := body
	for _, match := range hitmosFlightData.FindAllStringSubmatch(body, -1) {
		var payload string
		if json.Unmarshal([]byte(match[1]), &payload) == nil {
			data += "\n" + payload
		}
	}
	for _, loc := range hitmosTrackStart.FindAllStringIndex(data, -1) {
		var track struct {
			ID       int64  `json:"id"`
			Title    string `json:"title"`
			Artist   string `json:"artist"`
			Download string `json:"download"`
			Play     string `json:"play"`
		}
		if json.NewDecoder(strings.NewReader(data[loc[0]:])).Decode(&track) != nil || track.ID != id {
			continue
		}
		links := []string{}
		for _, raw := range []string{track.Download, track.Play} {
			if u, err := url.Parse(raw); err == nil && webURL(u) {
				links = append(links, raw)
			}
		}
		return links, strings.TrimSpace(track.Artist + " — " + track.Title), true
	}
	return nil, "", false
}

type siteDefinition struct {
	host     string
	template string
	path     bool
}

var directSites = []siteDefinition{
	{"hitmos.fm", "https://hitmos.fm/search?q=%s", false},
	{"hitmos.me", "https://hitmos.me/search?q=%s", false},
	{"hitmo.net", "https://hitmo.net/search?q=%s", false},
	{"lightaudio.ru", "https://lightaudio.ru/mp3/%s", true},
	{"lmusic.kz", "https://lmusic.kz/search?q=%s", false},
	{"drivemusic.me", "https://drivemusic.me/?do=search&subaction=search&story=%s", false},
	{"mp3mn.net", "https://mp3mn.net/?song=%s", false},
	{"bazamp3.com", "https://bazamp3.com/search?q=%s", false},
	{"mp3deer.cc", "https://mp3deer.cc/search.php?q=%s", false},
	{"zaycev.net", "https://zaycev.net/search.html?query_search=%s", false},
	{"zvuch.com", "https://zvuch.com/search?search=%s", false},
	{"mp3ha.org", "https://mp3ha.org/?q=%s", false},
	{"mp3tornado.net", "https://mp3tornado.net/tracks?q=%s", false},
	{"mp3track.online", "https://mp3track.online/song?q=%s", false},
	{"mp3tm.net", "https://mp3tm.net/?song=%s", false},
	{"mp3-blue.ru", "https://mp3-blue.ru/?mp3=%s", false},
	{"mp3party.net", "https://mp3party.net/search?q=%s", false},
	{"mp3uk.net", "https://mp3uk.net/search/%s", true},
	{"mp3rax.me", "https://mp3rax.me/search/%s", true},
	{"muzhot.org", "https://muzhot.org/search/%s", true},
	{"muzdrop.net", "https://muzdrop.net/search/%s", true},
	{"muzfold.net", "https://muzfold.net/search/%s", true},
	{"musrocket.com", "https://musrocket.com/search/%s", true},
	{"melodz.net", "https://melodz.net/index.php?q=%s", false},
	{"audiodragon.org", "https://audiodragon.org/?mp3=%s", false},
	{"tuthit.org", "https://tuthit.org/search/%s", true},
	{"visara.ru", "https://visara.ru/search/%s", true},
	{"mp3muzz.cc", "https://mp3muzz.cc/search/%s", true},
	{"muzkik.com", "https://muzkik.com/search.php?q=%s", false},
}

func (s siteDefinition) searchURL(query string) string {
	encoded := url.QueryEscape(query)
	if s.path {
		encoded = url.PathEscape(query)
	}
	return strings.Replace(s.template, "%s", encoded, 1)
}

var queryTokens = regexp.MustCompile(`[\p{L}\p{N}]{2,}`)

// Hitmos supplies Cyrillic names with decomposed й/ё in its player data.
var trackCyrillicComposition = strings.NewReplacer("и\u0306", "й", "е\u0308", "ё")

func trackMatch(query string, c CensorCandidate) bool {
	tokens := queryTokens.FindAllString(trackCyrillicComposition.Replace(strings.ToLower(query)), -1)
	if len(tokens) == 0 {
		return false
	}
	audioEvidence := c.Metadata["audio_url"]
	if u, err := url.Parse(audioEvidence); err == nil {
		if decoded, err := url.PathUnescape(u.Path + " " + u.RawQuery); err == nil {
			audioEvidence = decoded
		}
	}
	sourceEvidence := c.Metadata["source_url"]
	if u, err := url.Parse(sourceEvidence); err == nil {
		if decoded, err := url.PathUnescape(u.Path); err == nil {
			sourceEvidence = decoded
		}
	}
	for _, raw := range []string{audioEvidence, c.Metadata["page_title"], sourceEvidence} {
		value := trackCyrillicComposition.Replace(strings.ToLower(raw))
		matched := true
		for _, token := range tokens {
			if !strings.Contains(value, token) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func sameSite(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	normalize := func(host string) string { return strings.TrimPrefix(strings.ToLower(host), "www.") }
	return normalize(a.Hostname()) == normalize(b.Hostname())
}

func resultLinks(base *url.URL, body, query string, limit int) []string {
	out := make([]string, 0, limit)
	seen := map[string]bool{}
	for _, a := range webAnchors.FindAllStringSubmatch(body, 500) {
		attrs := webAttrMap(a[1])
		raw := attrs["href"]
		u, err := url.Parse(raw)
		if err != nil || raw == "" {
			continue
		}
		u = base.ResolveReference(u)
		label := strings.Join([]string{html.UnescapeString(webTagStrip.ReplaceAllString(a[2], " ")), u.Path, u.RawQuery}, " ")
		probe := CensorCandidate{Metadata: map[string]string{"audio_url": label, "page_title": label, "source_url": u.String()}}
		if !webURL(u) || !sameSite(base, u) || !trackMatch(query, probe) || seen[u.String()] {
			continue
		}
		seen[u.String()] = true
		out = append(out, u.String())
		if len(out) == limit {
			break
		}
	}
	return out
}

func (n *nativeSource) sitePageLinks(ctx context.Context, source, query string) []string {
	res, err := n.webRequest(ctx, http.MethodGet, source)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	kind, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if res.StatusCode != http.StatusOK || kind != "text/html" {
		return nil
	}
	body, err := boundedBody(res, 2<<20)
	if err != nil {
		return nil
	}
	return resultLinks(res.Request.URL, string(body), query, 2)
}

func (n *nativeSource) searchSite(ctx context.Context, site siteDefinition, query string) []CensorCandidate {
	search := site.searchURL(query)
	pages := append([]string{search}, n.sitePageLinks(ctx, search, query)...)
	out := make([]CensorCandidate, 0, 4)
	seen := map[string]bool{}
	for _, page := range pages {
		candidates, err := n.webDiscover(ctx, page)
		if err != nil {
			if ctx.Err() != nil {
				return out
			}
			continue
		}
		for _, candidate := range candidates {
			if seen[candidate.URL] || !trackMatch(query, candidate) {
				continue
			}
			seen[candidate.URL] = true
			candidate.Metadata["site"] = site.host
			candidate.Metadata["discovery_provider"] = "direct_web"
			candidate.Metadata["track_match"] = "true"
			out = append(out, candidate)
		}
	}
	return out
}

type siteProgressCallback func(site string, completed, total int) error
type siteProgressContextKey struct{}

func withSiteProgress(ctx context.Context, callback siteProgressCallback) context.Context {
	return context.WithValue(ctx, siteProgressContextKey{}, callback)
}

func (n *nativeSource) siteSearch(ctx context.Context, query string) ([]CensorCandidate, error) {
	if !validCensorQuery(query) {
		return nil, ErrCensorInvalid
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	results := make([][]CensorCandidate, len(directSites))
	done := make(chan int, len(directSites))
	var wg sync.WaitGroup
	for i, site := range directSites {
		i, site := i, site
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = n.searchSite(ctx, site, query)
			done <- i
		}()
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	completed := 0
	for index := range done {
		completed++
		if callback, ok := ctx.Value(siteProgressContextKey{}).(siteProgressCallback); ok {
			if err := callback(directSites[index].host, completed, len(directSites)); err != nil {
				cancel()
				return nil, err
			}
		}
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}

	out := make([]CensorCandidate, 0, 20)
	seen := map[string]bool{}
	for _, batch := range results {
		for _, candidate := range batch {
			if seen[candidate.URL] {
				continue
			}
			seen[candidate.URL] = true
			out = append(out, candidate)
			if len(out) == 20 {
				return out, nil
			}
		}
	}
	return out, nil
}
