package censor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"time"
)

type Engine struct {
	config Config
	client *http.Client
}

func NewEngine(c Config) (*Engine, error) {
	c, err := normalize(c)
	if err != nil {
		return nil, err
	}
	if c.StateDir != "" {
		c.StateDir, err = filepath.Abs(c.StateDir)
		if err != nil {
			return nil, err
		}
	}

	if strings.ContainsRune(c.FPcalcCommand, '/') {
		c.FPcalcCommand, err = filepath.Abs(c.FPcalcCommand)
		if err != nil {
			return nil, err
		}
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	transport := &http.Transport{TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableKeepAlives: true}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("censor: DNS returned no addresses")
		}
		for _, ip := range ips {
			if !c.AllowPrivateHTTP && !publicIP(ip.IP) {
				return nil, errors.New("censor: private or reserved HTTP address rejected")
			}
		}
		var last error
		for _, ip := range ips {
			conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	client := &http.Client{Timeout: c.HTTPTimeout, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("censor: too many redirects")
		}
		if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("censor: insecure redirect")
		}
		return validateURL(req.URL)
	}}
	return &Engine{c, client}, nil
}
func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		_, block, _ := net.ParseCIDR(s)
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
func validateURL(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return errors.New("censor: invalid HTTP URL")
	}
	return nil
}
func (e *Engine) acquire(ctx context.Context, s Source, dir, name string) (string, error) {
	var reader io.ReadCloser
	if s.Path != "" {
		f, err := os.Open(s.Path)
		if err != nil {
			return "", err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return "", err
		}
		if !st.Mode().IsRegular() || st.Size() > e.config.MaxBytes {
			f.Close()
			return "", errors.New("censor: input is not a bounded regular file")
		}
		reader = f
	} else {
		u, err := url.Parse(s.URL)
		if err != nil {
			return "", err
		}
		if err = validateURL(u); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := e.client.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return "", fmt.Errorf("censor: HTTP status %d", resp.StatusCode)
		}
		if contentType := resp.Header.Get("Content-Type"); contentType != "" {
			mediaType, _, parseErr := mime.ParseMediaType(contentType)
			if parseErr != nil || !(strings.HasPrefix(mediaType, "audio/") || strings.HasPrefix(mediaType, "video/") || mediaType == "application/octet-stream" || mediaType == "application/ogg" || mediaType == "application/x-flac") {
				resp.Body.Close()
				return "", errors.New("censor: HTTP content type is not audio media")
			}
		}
		if resp.ContentLength > e.config.MaxBytes {
			resp.Body.Close()
			return "", errors.New("censor: HTTP content exceeds limit")
		}
		if enc := resp.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
			resp.Body.Close()
			return "", errors.New("censor: encoded HTTP body rejected")
		}
		reader = resp.Body
	}
	defer reader.Close()
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(&contextReader{ctx, reader}, e.config.MaxBytes+1))
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n == 0 || n > e.config.MaxBytes {
		return "", errors.New("censor: empty input or byte limit exceeded")
	}
	return path, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type cappedBuffer struct {
	data     []byte
	limit    int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (e *Engine) decode(ctx context.Context, path, wavPath string) (features, error) {
	return e.decodeSegment(ctx, path, wavPath, 0, 0)
}
func (e *Engine) decodeSegment(ctx context.Context, path, wavPath string, start, duration float64) (features, error) {
	args := []string{"-nostdin", "-filter_threads", strconv.Itoa(e.config.CPUThreads), "-filter_complex_threads", strconv.Itoa(e.config.CPUThreads), "-hide_banner", "-loglevel", "error", "-max_alloc", "67108864", "-probesize", "5242880", "-analyzeduration", "5000000", "-threads", strconv.Itoa(e.config.CPUThreads), "-protocol_whitelist", "file,pipe", "-format_whitelist", "mp3,wav,flac,ogg,mov,matroska,webm,aac,aiff,asf", "-i", path}
	if duration > 0 {
		args = append(args, "-ss", strconv.FormatFloat(start, 'f', 6, 64), "-t", strconv.FormatFloat(duration, 'f', 6, 64))
	}
	args = append(args, "-map", "0:a:0", "-vn", "-sn", "-dn", "-ac", "1", "-ar", strconv.Itoa(e.config.SampleRate), "-threads", strconv.Itoa(e.config.CPUThreads), "-f", "s16le", "pipe:1")
	cmd := exec.CommandContext(ctx, e.config.FFmpeg, args...)
	boundCommand(cmd)
	stderr := &cappedBuffer{limit: 8192}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return features{}, err
	}
	var wav *os.File
	var writer io.Writer
	if wavPath != "" {
		wav, err = os.OpenFile(wavPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return features{}, err
		}
		defer wav.Close()
		if _, err = wav.Write(make([]byte, 44)); err != nil {
			return features{}, err
		}
		writer = wav
	}
	if err = cmd.Start(); err != nil {
		return features{}, fmt.Errorf("censor: start FFmpeg: %w", err)
	}
	maxSamples := int64(e.config.MaxDuration.Seconds() * float64(e.config.SampleRate))
	if duration > 0 {
		maxSamples = min(maxSamples, int64(math.Ceil(duration*float64(e.config.SampleRate)))+1)
	}
	f, extractErr := extract(ctx, out, e.config.SampleRate, maxSamples, writer)
	if extractErr != nil {
		cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return f, ctx.Err()
	}
	if extractErr != nil {
		return f, extractErr
	}
	if waitErr != nil {
		return f, fmt.Errorf("censor: FFmpeg: %w: %s", waitErr, strings.TrimSpace(string(stderr.data)))
	}
	if wav != nil {
		if err = writeWAVHeader(wav, f.samples, e.config.SampleRate); err != nil {
			return f, err
		}
	}
	return f, nil
}
func writeWAVHeader(w io.WriteSeeker, samples int64, rate int) error {
	if samples*2 > int64(^uint32(0))-36 {
		return errors.New("censor: WAV exceeds RIFF limit")
	}
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(samples*2+36))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(samples*2))
	if _, err := w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := w.Write(h)
	return err
}
func (e *Engine) Analyze(ctx context.Context, r Request) (Result, error) {
	if err := validateRequest(r); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, e.config.JobTimeout)
	defer cancel()
	workRoot, err := audioWorkRoot(e.config.StateDir)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(workRoot, 0700); err != nil {
		return Result{}, err
	}
	dir, err := os.MkdirTemp(workRoot, "censor-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	source, err := e.acquire(ctx, r.Source, dir, "source")
	if err != nil {
		return Result{}, err
	}
	f, err := e.decode(ctx, source, "")
	if err != nil {
		return Result{}, err
	}
	intervals, truncated := detectIntervals(f, e.config.MaxSuspiciousIntervals)
	result := Result{DurationSeconds: float64(f.samples) / float64(f.rate), Waveform: waveform(f, e.config.MaxWaveformPoints), SuspiciousIntervals: intervals, IntervalsTruncated: truncated, Warnings: []string{"Silence/beep intervals are heuristic candidates, not confirmed censorship"}}
	if e.config.FPcalcCommand != "" {
		result.Fingerprint, err = e.fingerprint(ctx, source, dir)
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if err != nil {
			result.Warnings = append(result.Warnings, "Chromaprint unavailable: "+err.Error())
		}
	}

	if r.Reference != nil {
		path, err := e.acquire(ctx, *r.Reference, dir, "reference")
		if err != nil {
			return Result{}, err
		}
		ref, err := e.decode(ctx, path, "")
		if err != nil {
			return Result{}, err
		}
		c, err := compare(ctx, f, ref)
		if err != nil {
			return Result{}, err
		}

		if e.config.FPcalcCommand != "" {
			fp, fpErr := e.fingerprint(ctx, path, dir)
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			if fpErr != nil {
				result.Warnings = append(result.Warnings, "Reference Chromaprint unavailable: "+fpErr.Error())
			} else {
				applyFingerprint(&c, result.Fingerprint, fp)
			}
		}
		result.Comparison = &c
		localRestoration(f, ref, c, result.SuspiciousIntervals)
	}

	return result, nil
}
