package bridge

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestKeywordAssetMatches(t *testing.T) {
	b, err := os.ReadFile("../../assets/drug-keywords.json")
	if err != nil {
		t.Fatal(err)
	}
	var a, c []string
	_ = json.Unmarshal(b, &a)
	_ = json.Unmarshal([]byte(keywordJSON), &c)
	if !reflect.DeepEqual(a, c) {
		t.Fatal("compiled keywords drifted from asset")
	}
}
func TestAnnotations(t *testing.T) {
	d := NewDetector()
	for _, tc := range []struct {
		text   string
		hit    bool
		marked []string
	}{
		{"weed", true, []string{"weed"}}, {"WEED, КОЛЁСА!", true, []string{"WEED", "КОЛЁСА"}},
		{"weedless", false, nil}, {"_weed weed2 наркотиками", false, nil}, {"éweedé", true, []string{"weed"}},
		{"weed weedless xre-upz", true, []string{"weed", "weedless", "xre-upz"}},
		{"re-up", true, []string{"re-up"}}, {"weed weedre-up", true, []string{"weed", "weedre-up"}}, {"xre-up", false, nil}, {"<weed> &", true, []string{"weed"}},
		{"KETAMINE", false, nil}, {"İNEEDLE", true, []string{"NEEDLE"}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			l := d.Annotate(tc.text)
			if l.IsDrug != tc.hit {
				t.Fatalf("hit=%v", l.IsDrug)
			}
			var marked []string
			var joined strings.Builder
			for _, p := range l.Parts {
				joined.WriteString(p.Text)
				if p.Marked {
					marked = append(marked, p.Text)
				}
			}
			if joined.String() != tc.text || !reflect.DeepEqual(marked, tc.marked) {
				t.Fatalf("parts=%+v", l.Parts)
			}
		})
	}
}
func TestStatuses(t *testing.T) {
	d := NewDetector()
	ts := "00:01"
	for _, tc := range []struct {
		lyrics []Lyric
		status string
		all    int
	}{
		{nil, "error", 0}, {[]Lyric{}, "none", 0}, {[]Lyric{{Text: "hello"}}, "none", 0},
		{[]Lyric{{Text: "weed"}, {Text: "hello"}}, "found", 2}, {[]Lyric{{TS: &ts, Text: "weed"}}, "found", 0},
	} {
		r := d.Process(Track{ID: 1}, tc.lyrics, true)
		if r.Status != tc.status || len(r.AllLines) != tc.all || !r.Ignored {
			t.Fatalf("%+v", r)
		}
	}
}
