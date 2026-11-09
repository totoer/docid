package docid

import (
	"sort"
	"strings"

	"github.com/google/btree"
)

// TextIndex defines a positional text index over string-valued JSON paths.
// Tokenizer is an FTS5 unicode61 specification; the empty value uses unicode61.
// Definitions are copied at API boundaries and are immutable after creation.
type TextIndex struct {
	Name      string   `json:"name"`
	Fields    []string `json:"fields"`
	Tokenizer string   `json:"tokenizer,omitempty"`
}

type textEntry struct {
	token    string
	field    int
	id       string
	position int
}
type textIndex struct {
	def       TextIndex
	tokenizer *unicodeTokenizer
	tree      *btree.BTreeG[textEntry]
}

func newTextIndex(def TextIndex) (*textIndex, error) {
	if !validName(def.Name) || len(def.Fields) == 0 {
		return nil, ErrInvalidName
	}
	seen := map[string]bool{}
	for _, f := range def.Fields {
		key := asciiLower(f)
		if !validPath(f) || seen[key] {
			return nil, ErrInvalidName
		}
		seen[key] = true
	}
	tok, err := newUnicodeTokenizer(def.Tokenizer)
	if err != nil {
		return nil, err
	}
	def.Fields = append([]string(nil), def.Fields...)
	if def.Tokenizer == "" {
		def.Tokenizer = "unicode61"
	}
	return &textIndex{def: def, tokenizer: tok, tree: btree.NewG(32, func(a, b textEntry) bool {
		if a.token != b.token {
			return a.token < b.token
		}
		if a.field != b.field {
			return a.field < b.field
		}
		if a.id != b.id {
			return a.id < b.id
		}
		return a.position < b.position
	})}, nil
}
func (idx *textIndex) clone() *textIndex { c := *idx; c.tree = idx.tree.Clone(); return &c }
func (idx *textIndex) entries(ctx operationContext, id string, doc Document) ([]textEntry, error) {
	var entries []textEntry
	for field, path := range idx.def.Fields {
		value, _ := lookup(doc, path)
		text, ok := value.(string)
		if !ok {
			continue
		}
		tokens, err := idx.tokenizer.tokenize(ctx, text)
		if err != nil {
			return nil, err
		}
		for pos, token := range tokens {
			entries = append(entries, textEntry{token: token, field: field, id: id, position: pos})
		}
	}
	return entries, nil
}
func (idx *textIndex) add(id string, doc Document) error {
	entries, err := idx.entries(nil, id, doc)
	if err != nil {
		return err
	}
	idx.apply(nil, entries)
	return nil
}
func (idx *textIndex) apply(old, next []textEntry) {
	for _, e := range old {
		idx.tree.Delete(e)
	}
	for _, e := range next {
		idx.tree.ReplaceOrInsert(e)
	}
}

type textLocation struct {
	id    string
	field int
}
type textPositions map[textLocation][]int
type textIDs map[string]struct{}

func sortedTextCandidates(ids textIDs) []string {
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func (idx *textIndex) positions(ctx operationContext, token string, prefix bool, fields []bool) (textPositions, error) {
	out := textPositions{}
	var err error
	idx.tree.AscendGreaterOrEqual(textEntry{token: token, field: -1}, func(e textEntry) bool {
		if prefix {
			if !strings.HasPrefix(e.token, token) {
				return false
			}
		} else if e.token != token {
			return false
		}
		if err = ctx.Err(); err != nil {
			return false
		}
		if fields[e.field] {
			key := textLocation{e.id, e.field}
			out[key] = append(out[key], e.position)
		}
		return true
	})
	for key, positions := range out {
		sort.Ints(positions)
		out[key] = positions
	}
	return out, err
}

type compiledTextTerm struct {
	token  string
	prefix bool
}

func (idx *textIndex) phrasePositions(ctx operationContext, phrase textPhrase, fields []bool) (textPositions, int, error) {
	var terms []compiledTextTerm
	for _, piece := range phrase.pieces {
		tokens, err := idx.tokenizer.tokenize(ctx, piece.value)
		if err != nil {
			return nil, 0, err
		}
		for _, token := range tokens {
			terms = append(terms, compiledTextTerm{token: token})
		}
		if len(terms) > 0 {
			terms[len(terms)-1].prefix = piece.prefix
		}
	}
	if len(terms) == 0 {
		return textPositions{}, 0, nil
	}
	positions, err := idx.positions(ctx, terms[0].token, terms[0].prefix, fields)
	if err != nil {
		return nil, 0, err
	}
	if phrase.initial {
		for key, list := range positions {
			if len(list) > 0 && list[0] == 0 {
				positions[key] = list[:1]
			} else {
				delete(positions, key)
			}
		}
	}
	for _, term := range terms[1:] {
		next, err := idx.positions(ctx, term.token, term.prefix, fields)
		if err != nil {
			return nil, 0, err
		}
		for key, list := range positions {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			other := next[key]
			matched := make([]int, 0)
			j := 0
			for _, end := range list {
				for j < len(other) && other[j] < end+1 {
					j++
				}
				if j < len(other) && other[j] == end+1 {
					matched = append(matched, end+1)
				}
			}
			if len(matched) == 0 {
				delete(positions, key)
			} else {
				positions[key] = matched
			}
		}
	}
	return positions, len(terms), nil
}

// nearPositions follows FTS5's end-position window: every phrase must end
// between maxEnd-len(phrase)-distance and maxEnd, within the same field.
func nearPositions(ctx operationContext, lists [][]int, lengths []int, distance int) (bool, error) {
	pointers := make([]int, len(lists))
	maximum := lists[0][0]
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		match := true
		for i, list := range lists {
			minimum := maximum - lengths[i] - distance
			for pointers[i] < len(list) && list[pointers[i]] < minimum {
				pointers[i]++
			}
			if pointers[i] == len(list) {
				return false, nil
			}
			if end := list[pointers[i]]; end > maximum {
				maximum = end
				match = false
			}
		}
		if match {
			return true, nil
		}
	}
}

func (idx *textIndex) evaluate(ctx operationContext, n *textNode, fields []bool) (textIDs, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if n.kind == "filter" {
		allowed := make([]bool, len(fields))
		copy(allowed, fields)
		for i, field := range idx.def.Fields {
			listed := false
			for _, name := range n.fields {
				if asciiLower(name) == asciiLower(field) {
					listed = true
					break
				}
			}
			allowed[i] = allowed[i] && (listed != n.exclude)
		}
		return idx.evaluate(ctx, n.left, allowed)
	}
	if n.kind != "near" {
		a, ae, err := idx.evaluate(ctx, n.left, fields)
		if err != nil {
			return nil, false, err
		}
		b, be, err := idx.evaluate(ctx, n.right, fields)
		if err != nil {
			return nil, false, err
		}
		if n.kind == "implicit" {
			if ae {
				return b, be, nil
			}
			if be {
				return a, ae, nil
			}
		}
		out := textIDs{}
		for id := range a {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			_, inB := b[id]
			if n.kind == "OR" || n.kind == "NOT" && !inB || (n.kind == "AND" || n.kind == "implicit") && inB {
				out[id] = struct{}{}
			}
		}
		if n.kind == "OR" {
			for id := range b {
				if err := ctx.Err(); err != nil {
					return nil, false, err
				}
				out[id] = struct{}{}
			}
		}
		return out, false, nil
	}
	var all []textPositions
	var lengths []int
	for _, phrase := range n.phrases {
		positions, length, err := idx.phrasePositions(ctx, phrase, fields)
		if err != nil {
			return nil, false, err
		}
		// FTS5 discards empty phrases from a NEAR group.
		if length != 0 {
			all = append(all, positions)
			lengths = append(lengths, length)
		}
	}
	out := textIDs{}
	if len(all) == 0 {
		return out, true, nil
	}
	for key, list := range all[0] {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		lists := make([][]int, len(all))
		lists[0] = list
		found := true
		for i := 1; i < len(all); i++ {
			lists[i] = all[i][key]
			if len(lists[i]) == 0 {
				found = false
				break
			}
		}
		if !found {
			continue
		}
		match, err := nearPositions(ctx, lists, lengths, n.distance)
		if err != nil {
			return nil, false, err
		}
		if match {
			out[key.id] = struct{}{}
		}
	}
	return out, false, nil
}
func (idx *textIndex) search(ctx operationContext, n *textNode) (textIDs, error) {
	if err := n.validateFields(idx.def.Fields); err != nil {
		return nil, err
	}
	fields := make([]bool, len(idx.def.Fields))
	for i := range fields {
		fields[i] = true
	}
	ids, _, err := idx.evaluate(ctx, n, fields)
	return ids, err
}
