package bridge

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type nativeYandexTrack struct {
	ID      yandexID `json:"id"`
	Title   string   `json:"title"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	DurationMS int64 `json:"durationMs"`
	Explicit   bool  `json:"explicit"`
	Available  *bool `json:"available"`
}

func (t nativeYandexTrack) artist() string {
	names := []string{}
	for _, a := range t.Artists {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}
func (c *yandexClient) nativeTrack(ctx context.Context, id int64) (nativeYandexTrack, error) {
	t, err := c.nativeTrackMetadata(ctx, id)
	if err == nil && t.Available != nil && !*t.Available {
		return nativeYandexTrack{}, errors.New("Yandex track is unavailable for this account")
	}
	return t, err
}

// Metadata remains useful for discovery even when the account cannot play audio.
func (c *yandexClient) nativeTrackMetadata(ctx context.Context, id int64) (nativeYandexTrack, error) {
	var tracks []nativeYandexTrack
	if id <= 0 {
		return nativeYandexTrack{}, ErrCensorNotFound
	}
	if err := c.request(ctx, http.MethodGet, "/tracks/"+strconv.FormatInt(id, 10), "", &tracks); err != nil {
		return nativeYandexTrack{}, err
	}
	for _, t := range tracks {
		if string(t.ID) == strconv.FormatInt(id, 10) {

			return t, nil
		}
	}
	return nativeYandexTrack{}, ErrCensorNotFound
}

func nativeYandexURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && u.User == nil && u.Fragment == "" && (u.Port() == "" || u.Port() == "443") && (strings.HasSuffix(host, ".yandex.net") || strings.HasSuffix(host, ".yandex.ru"))
}
func (c *yandexClient) nativeFullMP3(ctx context.Context, id int64) (string, error) {
	var infos []struct {
		Codec   string `json:"codec"`
		Preview bool   `json:"preview"`
		DRM     bool   `json:"drm"`
		Bitrate int    `json:"bitrateInKbps"`
		URL     string `json:"downloadInfoUrl"`
	}
	if id <= 0 {
		return "", ErrCensorNotFound
	}
	if err := c.request(ctx, http.MethodGet, "/tracks/"+strconv.FormatInt(id, 10)+"/download-info", "", &infos); err != nil {
		return "", err
	}
	best, bitrate := "", -1
	for _, info := range infos {
		if info.Codec == "mp3" && !info.Preview && !info.DRM && nativeYandexURL(info.URL) && info.Bitrate > bitrate {
			best, bitrate = info.URL, info.Bitrate
		}
	}
	if best == "" {
		return "", errors.New("Yandex offers no authorized full non-DRM MP3 (preview audio is not accepted)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, best, nil)
	if err != nil {
		return "", errors.New("invalid Yandex download descriptor URL")
	}
	// Signed descriptors do not receive OAuth headers; do not follow redirects.
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return "", errors.New("Yandex download descriptor request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", yandexHTTPError(res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return "", errors.New("invalid or oversized Yandex download descriptor")
	}
	return nativeYandexMediaURL(b)
}

var nativeTimestamp = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)

func nativeYandexMediaURL(b []byte) (string, error) {
	var d struct {
		Host string `json:"host" xml:"host"`
		Path string `json:"path" xml:"path"`
		TS   string `xml:"ts"`
		Salt string `json:"s" xml:"s"`
	}
	if strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
		var j struct {
			Host string          `json:"host"`
			Path string          `json:"path"`
			TS   json.RawMessage `json:"ts"`
			Salt string          `json:"s"`
		}
		if json.Unmarshal(b, &j) != nil {
			return "", errors.New("invalid Yandex JSON descriptor")
		}
		d.Host, d.Path, d.Salt = j.Host, j.Path, j.Salt
		if json.Unmarshal(j.TS, &d.TS) != nil {
			var number json.Number
			if json.Unmarshal(j.TS, &number) != nil {
				return "", errors.New("invalid Yandex descriptor timestamp")
			}
			d.TS = string(number)
		}
	} else if xml.Unmarshal(b, &d) != nil {
		return "", errors.New("invalid Yandex XML descriptor")
	}
	origin, err := url.Parse("https://" + d.Host)
	if err != nil || !nativeYandexURL("https://"+d.Host) || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery {
		return "", errors.New("untrusted Yandex descriptor host")
	}
	if !strings.HasPrefix(d.Path, "/") || strings.ContainsAny(d.Path, "#\r\n\t") || !nativeTimestamp.MatchString(d.TS) || d.TS == "." || d.TS == ".." || d.Salt == "" {
		return "", errors.New("invalid Yandex descriptor fields")
	}
	// Match the existing Qt player and upstream DownloadInfo signing formula;
	// preserve raw query escapes and opaque (including hexadecimal) timestamps.
	digest := md5.Sum([]byte("XGRlBW9FXlekgbPrRHuSiA" + d.Path[1:] + d.Salt))
	media := fmt.Sprintf("https://%s/get-mp3/%x/%s%s", d.Host, digest, d.TS, d.Path)
	if !nativeYandexURL(media) {
		return "", errors.New("invalid Yandex signed audio URL")
	}
	return media, nil
}
