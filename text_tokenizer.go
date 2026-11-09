package docid

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// unicodeTokenizer is immutable after construction and safe to share between
// readers and COW index snapshots. Its tables intentionally use Unicode 6.1.
type unicodeTokenizer struct {
	remove     int
	categories [32]bool
	ascii      [128]bool
	exceptions map[rune]bool
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func ftsWhitespace(c byte) bool {
	return c == ' ' || c >= '\t' && c <= '\r'
}

func ftsBareword(c byte) bool {
	return c >= 128 || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == 0x1a
}

func tokenizerArguments(spec string) ([]string, error) {
	if spec == "" {
		return []string{"unicode61"}, nil
	}
	if !utf8.ValidString(spec) || strings.ContainsRune(spec, 0) {
		return nil, ErrInvalidName
	}
	var args []string
	for i := 0; i < len(spec); {
		if ftsWhitespace(spec[i]) {
			i++
			continue
		}
		if spec[i] == '\'' {
			i++
			var word strings.Builder
			closed := false
			for i < len(spec) {
				if spec[i] == '\'' {
					i++
					if i == len(spec) || spec[i] != '\'' {
						closed = true
						break
					}
				}
				word.WriteByte(spec[i])
				i++
			}
			if !closed {
				return nil, ErrInvalidName
			}
			args = append(args, word.String())
		} else {
			start := i
			for i < len(spec) && ftsBareword(spec[i]) {
				i++
			}
			if start == i {
				return nil, ErrInvalidName
			}
			args = append(args, spec[start:i])
		}
	}
	return args, nil
}

var unicodeCategoryNames = [...]string{"", "Cc", "Cf", "Cn", "Cs", "Ll", "Lm", "Lo", "Lt", "Lu", "Mc", "Me", "Mn", "Nd", "Nl", "No", "Pc", "Pd", "Pe", "Pf", "Pi", "Po", "Ps", "Sc", "Sk", "Sm", "So", "Zl", "Zp", "Zs", "LC", "Co"}

func newUnicodeTokenizer(spec string) (*unicodeTokenizer, error) {
	args, err := tokenizerArguments(spec)
	if err != nil || len(args)%2 != 1 || asciiLower(args[0]) != "unicode61" {
		return nil, fmt.Errorf("%w: invalid unicode61 specification", ErrInvalidName)
	}
	t := &unicodeTokenizer{remove: 1, exceptions: map[rune]bool{}}
	categories := "L* N* Co"
	for i := 1; i < len(args); i += 2 {
		if asciiLower(args[i]) == "categories" {
			categories = args[i+1]
		}
	}
	for _, word := range strings.FieldsFunc(categories, func(r rune) bool { return r == ' ' || r == '\t' }) {
		if len(word) < 2 {
			return nil, ErrInvalidName
		}
		found := false
		for i, category := range unicodeCategoryNames[1:] {
			if word[:2] == category || word[0] == category[0] && word[1] == '*' {
				t.categories[i+1] = true
				found = true
			}
		}
		if !found {
			return nil, ErrInvalidName
		}
		t.categories[0] = true // SQLite treats unclassified codepoints separately.
	}
	for r := rune(1); r < 128; r++ {
		t.ascii[r] = t.categories[unicode61Category(r)]
	}
	for i := 1; i < len(args); i += 2 {
		value := args[i+1]
		switch asciiLower(args[i]) {
		case "categories":
		case "remove_diacritics":
			if len(value) != 1 || value[0] < '0' || value[0] > '2' {
				return nil, ErrInvalidName
			}
			t.remove = int(value[0] - '0')
		case "tokenchars", "separators":
			want := asciiLower(args[i]) == "tokenchars"
			for _, r := range value {
				if r < 128 {
					t.ascii[r] = want
				} else if t.categories[unicode61Category(r)] != want && !unicode61Diacritic(r) {
					t.exceptions[r] = true
				}
			}
		default:
			return nil, ErrInvalidName
		}
	}
	return t, nil
}

func (t *unicodeTokenizer) tokenChar(r rune) bool {
	if r >= 0 && r < 128 {
		return t.ascii[r]
	}
	return t.categories[unicode61Category(r)] != t.exceptions[r]
}

// sqliteRune also preserves SQLite's treatment of noncharacters and malformed
// UTF-8 query bytes. Stored documents have already passed JSON validation.
func sqliteRune(s string, i int) (rune, int) {
	c := uint32(s[i])
	i++
	if c >= 0xc0 {
		switch {
		case c < 0xe0:
			c &= 31
		case c < 0xf0:
			c &= 15
		case c < 0xf8:
			c &= 7
		case c < 0xfc:
			c &= 3
		case c < 0xfe:
			c &= 1
		default:
			c = 0
		}
		for i < len(s) && s[i]&0xc0 == 0x80 {
			c = c<<6 + uint32(s[i]&63)
			i++
		}
		if c < 128 || c&0xfffff800 == 0xd800 || c&0xfffffffe == 0xfffe {
			c = 0xfffd
		}
	}
	return rune(c), i
}

// SQLite's UTF-8 writer also handles the non-scalar values its permissive
// decoder can produce for malformed query bytes. Go's utf8.AppendRune would
// replace those values and change the term being looked up.
func appendSQLiteRune(dst []byte, r rune) []byte {
	c := uint32(r)
	switch {
	case c < 0x80:
		return append(dst, byte(c))
	case c < 0x800:
		return append(dst, 0xc0+byte(c>>6&31), 0x80+byte(c&63))
	case c < 0x10000:
		return append(dst, 0xe0+byte(c>>12&15), 0x80+byte(c>>6&63), 0x80+byte(c&63))
	default:
		return append(dst, 0xf0+byte(c>>18&7), 0x80+byte(c>>12&63), 0x80+byte(c>>6&63), 0x80+byte(c&63))
	}
}

func (t *unicodeTokenizer) tokenize(ctx operationContext, text string) ([]string, error) {
	var tokens []string
	buf := make([]byte, 0, 64)
	inToken := false
	flush := func() {
		if inToken {
			tokens = append(tokens, string(buf))
			buf = buf[:0]
			inToken = false
		}
	}
	for i, polled := 0, 0; i < len(text); {
		if ctx != nil && i >= polled {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			polled = i + 1024
		}
		r, next := sqliteRune(text, i)
		i = next
		if !t.tokenChar(r) && !(inToken && r >= 128 && unicode61Diacritic(r)) {
			flush()
			continue
		}
		inToken = true
		r = unicode61Fold(r, t.remove)
		if r != 0 && len(buf) < 32768 {
			buf = appendSQLiteRune(buf, r)
			if len(buf) > 32768 {
				buf = buf[:32768]
			}
		}
	}
	flush()
	return tokens, nil
}
