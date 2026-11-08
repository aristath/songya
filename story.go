package songya

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"
)

type StoryPage struct {
	User          string       `json:"user,omitempty"`
	UserTruncated bool         `json:"user_truncated,omitempty"`
	ID            string       `json:"id"`
	Revision      string       `json:"-"`
	Text          string       `json:"text,omitempty"`
	Base64        string       `json:"base64,omitempty"`
	Offset        int          `json:"offset"`
	Next          int          `json:"next,omitempty"`
	Truncated     bool         `json:"truncated"`
	Unconfirmed   bool         `json:"unconfirmed,omitempty"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
	Parts         []StoryPart  `json:"parts,omitempty"`
}

// Start identifies this occurrence in the composed byte stream, including when
// the same memory appears twice. A part can continue on the next page.
type StoryPart struct {
	ID     string      `json:"id"`
	Kind   Destination `json:"kind"`
	Title  string      `json:"title,omitempty"`
	Start  int         `json:"start"`
	Text   string      `json:"text,omitempty"`
	Base64 string      `json:"base64,omitempty"`
}

// Compose expands the story’s ordered relations. Prose is never another link store.
func (s *Store) Compose(ctx context.Context, id string, offset, limit int) (StoryPage, error) {
	return s.compose(ctx, id, offset, limit, false)
}

// ComposeParts uses the same traversal and byte offsets as Compose, retaining
// record boundaries for readers. It does not copy prose into stored records.
func (s *Store) ComposeParts(ctx context.Context, id string, offset, limit int) (StoryPage, error) {
	return s.compose(ctx, id, offset, limit, true)
}

func (s *Store) compose(ctx context.Context, id string, offset, limit int, parts bool) (StoryPage, error) {
	catalog, err := s.Scan(ctx)
	if err != nil {
		return StoryPage{}, err
	}
	records := catalogMap(catalog)
	original, exists := records[id]
	if !exists {
		return StoryPage{}, fmt.Errorf("story %s is unavailable", id)
	}
	if original.Kind != Stories {
		return StoryPage{}, fmt.Errorf("%s is not a story", id)
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 4096
	}
	if limit > 16384 {
		limit = 16384
	}
	diagnostics := append([]Diagnostic{}, catalog.Diagnostics...)
	// Discard the prefix while streaming; a large offset must not allocate a
	// correspondingly large buffer. Extra bytes preserve UTF-8 page boundaries.
	ceiling := limit + 2*utf8.UTFMax
	validUTF8 := true
	skip := offset
	visits := 0
	var renderErr error
	var output strings.Builder
	type span struct {
		part     StoryPart
		from, to int
	}
	var spans []span
	write := func(value string, part *StoryPart) {
		validUTF8 = validUTF8 && utf8.ValidString(value)
		partStart := offset - skip + output.Len()
		if parts && part != nil && part.Kind == Memories && value == "" && skip == 0 {
			copy := *part
			copy.Start = partStart
			spans = append(spans, span{copy, output.Len(), output.Len()})
		}
		if skip >= len(value) {
			skip -= len(value)
			return
		}
		value = value[skip:]
		skip = 0
		remaining := ceiling - output.Len()
		if remaining > 0 {
			if len(value) > remaining {
				value = value[:remaining]
			}
			from := output.Len()
			output.WriteString(value)
			if parts && part != nil && len(value) > 0 {
				copy := *part
				copy.Start = partStart
				spans = append(spans, span{copy, from, output.Len()})
			}
		}
	}
	unconfirmed := original.Unconfirmed
	active := map[string]bool{}
	type position struct {
		id    string
		depth int
	}
	lengths := map[position]uint64{}
	var render func(string, int)
	render = func(current string, depth int) {
		if output.Len() >= ceiling || ctx.Err() != nil || renderErr != nil {
			return
		}
		visits++
		if visits > 100000 {
			renderErr = fmt.Errorf("story expansion exceeds the traversal budget; retrieve a nested story separately")
			return
		}
		if depth > 32 {
			diagnostics = append(diagnostics, Diagnostic{current, "recursive story reference retained"})
			write("[["+current+"]] [recursive reference]\n", &StoryPart{ID: current, Kind: kindOf(current)})
			return
		}
		resolution := resolve(records, current)
		unconfirmed = unconfirmed || resolution.Unconfirmed
		if resolution.Unconfirmed {
			diagnostics = append(diagnostics, resolution.Diagnostics...)
		}
		if resolution.Ambiguous || len(resolution.Targets) != 1 {
			diagnostics = append(diagnostics, resolution.Diagnostics...)
			diagnostics = append(diagnostics, Diagnostic{current, "reference needs resolution; replacements: " + strings.Join(resolution.Targets[:min(10, len(resolution.Targets))], ", ")})
			if r, ok := records[current]; ok {
				write(r.Body, &StoryPart{ID: r.ID, Kind: r.Kind, Title: clip(r.Title, 320)})
			} else {
				write("[["+current+"]] [missing record]\n", &StoryPart{ID: current, Kind: kindOf(current)})
			}
			return
		}
		current = resolution.Targets[0]
		if active[current] {
			diagnostics = append(diagnostics, Diagnostic{current, "recursive story reference retained"})
			write("[["+current+"]] [recursive reference]\n", &StoryPart{ID: current, Kind: records[current].Kind, Title: clip(records[current].Title, 320)})
			return
		}
		key := position{current, depth}
		if length, known := lengths[key]; known && length <= uint64(skip) {
			skip -= int(length)
			return
		}

		r := records[current]
		if r.Kind != Stories {
			lengths[key] = uint64(len(r.Body))
			write(r.Body, &StoryPart{ID: r.ID, Kind: r.Kind, Title: clip(r.Title, 320)})
			return
		}
		active[current] = true
		defer delete(active, current)
		startSkip, startOutput, startDiagnostics := skip, output.Len(), len(diagnostics)
		write(r.Body, &StoryPart{ID: r.ID, Kind: r.Kind, Title: clip(r.Title, 320)})
		for index, member := range storyMembers(r) {
			if output.Len() >= ceiling || ctx.Err() != nil || renderErr != nil {
				return
			}
			if r.Body != "" || index > 0 {
				write("\n\n", nil)
			}
			render(member, depth+1)
		}
		// Only completed, unambiguous expansions have reusable lengths. Keying by
		// depth keeps the recursion boundary identical across separate page requests.
		if output.Len() < ceiling && len(diagnostics) == startDiagnostics && renderErr == nil && ctx.Err() == nil {
			lengths[key] = uint64(startSkip-skip) + uint64(output.Len()-startOutput)
		}
	}
	render(id, 0)
	if err := ctx.Err(); err != nil {
		return StoryPage{}, err
	}
	if renderErr != nil {
		return StoryPage{}, renderErr
	}
	text := output.String()
	offset -= skip
	start := 0
	// Offsets are byte offsets. A manually supplied offset inside a rune advances
	// to the next boundary. Following Next always preserves the entire narrative.
	for validUTF8 && start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	end := min(start+limit, len(text))
	for validUTF8 && end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	if validUTF8 && end == start && start < len(text) {
		_, size := utf8.DecodeRuneInString(text[start:])
		end = start + size
	}
	if end > int(^uint(0)>>1)-offset {
		return StoryPage{}, fmt.Errorf("composed offset exceeds the byte range; retrieve a nested story separately")
	}
	page := StoryPage{User: clip(original.User, 160), UserTruncated: len(original.User) > 160, ID: id, Revision: original.Revision, Text: text[start:end], Offset: offset + start, Truncated: end < len(text), Unconfirmed: unconfirmed, Diagnostics: boundedDiagnostics(diagnostics)}
	if !utf8.ValidString(page.Text) {
		page.Base64 = base64.StdEncoding.EncodeToString([]byte(page.Text))
		page.Text = ""
	}
	if page.Truncated {
		page.Next = offset + end
	}
	if parts {
		page.Text, page.Base64 = "", ""
		page.Parts = []StoryPart{}
		for _, span := range spans {
			from, to := max(start, span.from), min(end, span.to)
			if from >= to {
				// An empty memory still has an identity in the reader. Emit it on
				// one page only, including at the end of an otherwise empty story.
				if span.from == span.to && span.from >= start && (span.from < end || (!page.Truncated && span.from == end)) {
					page.Parts = append(page.Parts, span.part)
				}
				continue
			}
			part := span.part
			part.Text = text[from:to]
			if !utf8.ValidString(part.Text) {
				part.Base64 = base64.StdEncoding.EncodeToString([]byte(part.Text))
				part.Text = ""
			}
			page.Parts = append(page.Parts, part)
		}
	}
	return page, nil
}
