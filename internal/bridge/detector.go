package bridge

import (
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
type Detector struct{ keywords []string }

func NewDetector() *Detector {
	var k []string
	if err := json.Unmarshal([]byte(keywordJSON), &k); err != nil {
		panic(err)
	}
	sort.SliceStable(k, func(i, j int) bool { return utf8.RuneCountInString(k[i]) > utf8.RuneCountInString(k[j]) })
	return &Detector{k}
}

// JavaScript's explicit word class, not Unicode's broader letter class.
func word(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r == 'ё' || r == 'Ё'
}
func (d *Detector) spans(text string, expand bool) [][2]int {
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
				for _, k := range d.keywords {
					n := utf8.RuneCountInString(k)
					if at+n <= len(r) && string(low[at:at+n]) == k {
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
		return out
	}

	for i := 0; i < len(r); {
		end := -1
		for _, k := range d.keywords {
			kr := []rune(k)
			n := len(kr)
			if i+n > len(r) {
				continue
			}
			if string(low[i:i+n]) != k {
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
	return out
}
func (d *Detector) Annotate(text string) Line {
	l := Line{IsDrug: len(d.spans(text, false)) > 0, Parts: []Part{{Text: text}}}
	if !l.IsDrug {
		return l
	}
	r := []rune(text)
	l.Parts = []Part{}
	offset := 0
	for _, s := range d.spans(text, true) {
		if s[0] > offset {
			l.Parts = append(l.Parts, Part{Text: string(r[offset:s[0]])})
		}
		l.Parts = append(l.Parts, Part{Text: string(r[s[0]:s[1]]), Marked: true})
		offset = s[1]
	}
	if offset < len(r) {
		l.Parts = append(l.Parts, Part{Text: string(r[offset:])})
	}
	return l
}
func (d *Detector) Process(t Track, lyrics []Lyric, ignored bool) Result {
	out := Result{Track: t, Status: "error", Ignored: ignored, Lyrics: lyrics, Lines: []Line{}}
	if lyrics == nil {
		return out
	}
	out.Status = "none"
	all := []Line{}
	unsynced := true
	for _, l := range lyrics {
		a := d.Annotate(l.Text)
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
	return out
}
