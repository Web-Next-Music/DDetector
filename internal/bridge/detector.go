package bridge

import (
	"context"
	"encoding/json"
	"sort"
	"unicode/utf8"
)

type Part struct {
	Text   string `json:"text"`
	Marked bool   `json:"marked"`
}
type Lyric struct {
	TS   *string `json:"ts"`
	Text string  `json:"text"`
}
type Line struct {
	TS     *string `json:"ts"`
	IsDrug bool    `json:"isDrug"`
	Parts  []Part  `json:"parts"`
}
type Track struct {
	ID      int64   `json:"id"`
	Title   string  `json:"title"`
	Artist  string  `json:"artist"`
	Cover   *string `json:"cover"`
	AddedAt *string `json:"added_at"`
}
type Result struct {
	Track
	Status   string  `json:"status"`
	Ignored  bool    `json:"ignored"`
	Lyrics   []Lyric `json:"lyrics"`
	Lines    []Line  `json:"lines"`
	AllLines []Line  `json:"allLines,omitempty"`
}
type Detector struct{ keywords map[rune][][]rune }

func NewDetector() *Detector {
	var k []string
	if err := json.Unmarshal([]byte(keywordJSON), &k); err != nil {
		panic(err)
	}
	sort.SliceStable(k, func(i, j int) bool { return utf8.RuneCountInString(k[i]) > utf8.RuneCountInString(k[j]) })
	compiled := make(map[rune][][]rune)
	for _, keyword := range k {
		runes := []rune(keyword)
		compiled[runes[0]] = append(compiled[runes[0]], runes)
	}
	return &Detector{compiled}
}

// JavaScript's explicit word class, not Unicode's broader letter class.
func word(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r == 'ё' || r == 'Ё'
}

func runeMatch(text, keyword []rune) bool {
	if len(text) < len(keyword) {
		return false
	}
	for i, r := range keyword {
		if text[i] != r {
			return false
		}
	}
	return true
}
func (d *Detector) spansContext(ctx context.Context, text string, expand bool) ([][2]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := []rune(text)
	low := append([]rune(nil), r...)
	for i, c := range low {
		if c >= 'A' && c <= 'Z' || c >= 'А' && c <= 'Я' {
			low[i] = c + 32
		} else if c == 'Ё' {
			low[i] = 'ё'
		}
	}
	out := [][2]int{}
	if expand {
		for start := 0; start < len(r); {
			if start%256 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if start > 0 && word(r[start-1]) {
				start++
				continue
			}
			prefix := start
			for prefix < len(r) && word(r[prefix]) {
				prefix++
			}
			end := -1
			for at := prefix; at >= start && end < 0; at-- {
				if at%256 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				if at >= len(low) {
					continue
				}
				for _, k := range d.keywords[low[at]] {
					n := len(k)
					if runeMatch(low[at:], k) {
						end = at + n
						for end < len(r) && word(r[end]) {
							end++
						}
						break
					}
				}
			}
			if end < 0 {
				start++
				continue
			}
			out = append(out, [2]int{start, end})
			start = end
		}
		return out, nil
	}

	for i := 0; i < len(r); {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		end := -1
		for _, k := range d.keywords[low[i]] {
			n := len(k)
			if i+n > len(r) {
				continue
			}
			if !runeMatch(low[i:], k) {
				continue
			}
			if !expand && (i > 0 && word(r[i-1]) || i+n < len(r) && word(r[i+n])) {
				continue
			}
			end = i + n
			break
		}
		if end < 0 {
			i++
			continue
		}
		start := i
		out = append(out, [2]int{start, end})
		i = end
	}
	return out, nil
}
func (d *Detector) Annotate(text string) Line {
	line, _ := d.annotateContext(context.Background(), text)
	return line
}
func (d *Detector) annotateContext(ctx context.Context, text string) (Line, error) {
	spans, err := d.spansContext(ctx, text, false)
	if err != nil {
		return Line{}, err
	}
	l := Line{IsDrug: len(spans) > 0, Parts: []Part{{Text: text}}}
	if !l.IsDrug {
		return l, nil
	}
	r := []rune(text)
	l.Parts = []Part{}
	offset := 0
	expanded, err := d.spansContext(ctx, text, true)
	if err != nil {
		return Line{}, err
	}
	for _, s := range expanded {
		if s[0] > offset {
			l.Parts = append(l.Parts, Part{Text: string(r[offset:s[0]])})
		}
		l.Parts = append(l.Parts, Part{Text: string(r[s[0]:s[1]]), Marked: true})
		offset = s[1]
	}
	if offset < len(r) {
		l.Parts = append(l.Parts, Part{Text: string(r[offset:])})
	}
	return l, nil
}
func (d *Detector) Process(t Track, lyrics []Lyric, ignored bool) Result {
	result, err := d.ProcessContext(context.Background(), t, lyrics, ignored)
	if err != nil {
		return Result{Track: t, Status: "error", Ignored: ignored, Lines: []Line{}}
	}
	return result
}
func (d *Detector) ProcessContext(ctx context.Context, t Track, lyrics []Lyric, ignored bool) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := validateLyrics(lyrics); err != nil {
		return Result{}, err
	}
	out := Result{Track: t, Status: "error", Ignored: ignored, Lyrics: lyrics, Lines: []Line{}}
	if lyrics == nil {
		return out, nil
	}
	out.Status = "none"
	all := []Line{}
	unsynced := true
	for _, l := range lyrics {
		a, err := d.annotateContext(ctx, l.Text)
		if err != nil {
			return Result{}, err
		}
		a.TS = l.TS
		all = append(all, a)
		if l.TS != nil {
			unsynced = false
		}
		if a.IsDrug {
			out.Lines = append(out.Lines, a)
			out.Status = "found"
		}
	}
	if out.Status == "found" && unsynced {
		out.AllLines = all
	}
	return out, nil
}
