package bridge

import "fmt"

// Budgets apply to expanded lyrics, not just the downloaded representation.
const (
	maxLyricsBytes      = 2 << 20
	maxLyricsLines      = 10000
	maxLyricLineBytes   = 16 << 10
	maxLRCStampsPerLine = 64
)

func validateLyrics(lines []Lyric) error {
	if len(lines) > maxLyricsLines {
		return fmt.Errorf("lyrics line budget exceeded")
	}
	total := 0
	for _, line := range lines {
		if len(line.Text) > maxLyricLineBytes {
			return fmt.Errorf("lyrics line too large")
		}
		total += len(line.Text)
		if line.TS != nil {
			total += len(*line.TS)
		}
		if total > maxLyricsBytes {
			return fmt.Errorf("expanded lyrics budget exceeded")
		}
	}
	return nil
}
