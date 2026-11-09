package songya

import (
	"math"
	"strconv"
)

// ProfilePicture refers to an original attachment. Cropping never changes its bytes.
type ProfilePicture struct {
	Filename string  `json:"filename" yaml:"filename"`
	X        float64 `json:"x" yaml:"x"`
	Y        float64 `json:"y" yaml:"y"`
	Zoom     float64 `json:"zoom" yaml:"zoom"`
}

func (d *Document) ProfilePicture() *ProfilePicture {
	node := metadataField(&d.Frontmatter, "profile_picture")
	if node == nil {
		return nil
	}
	var values map[string]any
	if node.Decode(&values) != nil {
		return nil
	}
	filename, _ := values["filename"].(string)
	if _, err := attachmentName(filename); err != nil {
		return nil
	}
	number := func(key string, fallback, low, high float64) float64 {
		var n float64
		switch v := values[key].(type) {
		case float64:
			n = v
		case int:
			n = float64(v)
		case string:
			var err error
			n, err = strconv.ParseFloat(v, 64)
			if err != nil {
				return fallback
			}
		default:
			return fallback
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return fallback
		}
		return math.Max(low, math.Min(high, n))
	}
	return &ProfilePicture{Filename: filename, X: number("x", .5, 0, 1), Y: number("y", .5, 0, 1), Zoom: number("zoom", 1, 1, 5)}
}
