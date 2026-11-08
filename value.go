package docid

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
	"unicode/utf8"
)

// Document is a JSON object. Decoded numbers are json.Number to retain precision.
type Document map[string]any

func decodeDocument(raw []byte) (Document, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing JSON", ErrInvalidDocument)
	}
	obj, ok := value.(map[string]any)
	if !ok || obj == nil {
		return nil, ErrInvalidDocument
	}
	return Document(obj), nil
}

func validateDocument(raw []byte) error {
	object := bytes.TrimSpace(raw)
	if len(object) == 0 || object[0] != '{' || !json.Valid(raw) {
		return ErrInvalidDocument
	}
	return nil
}

func encodeDocument(value any) ([]byte, Document, error) {
	raw, err := marshalDocument(value)
	if err != nil {
		return nil, nil, err
	}
	doc, err := decodeDocument(raw)
	return raw, doc, err
}

// Validate the object without decoding a map when no index needs one. Validation
// also enforces the decoder's depth limit on JSON produced from Go containers.
func marshalDocument(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	if err := validateDocument(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func lookup(doc Document, path string) (any, bool) {
	var value any = map[string]any(doc)
	for {
		part, rest, more := strings.Cut(path, ".")
		obj, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = obj[part]
		if !ok {
			return nil, false
		}
		if !more {
			return value, true
		}
		path = rest
	}
}

func validPath(path string) bool {
	if !utf8.ValidString(path) {
		return false
	}
	for {
		part, rest, more := strings.Cut(path, ".")
		if part == "" {
			return false
		}
		if !more {
			return true
		}
		path = rest
	}
}

// Scalars have a total order: null, bool, number, string. Decimal comparison is
// exact and does not expand exponents, even for numbers such as 1e1000000000.
type scalar struct {
	kind  int
	text  string
	sign  int
	order *big.Int
}

func scalarOf(value any) (scalar, bool) {
	switch v := value.(type) {
	case nil:
		return scalar{}, true
	case bool:
		if v {
			return scalar{kind: 1, text: "1"}, true
		}
		return scalar{kind: 1, text: "0"}, true
	case string:
		return scalar{kind: 3, text: v}, true
	case json.Number:
		s := string(v)
		sign := 1
		if strings.HasPrefix(s, "-") {
			sign = -1
			s = s[1:]
		}
		exp := new(big.Int)
		if pos := strings.IndexAny(s, "eE"); pos >= 0 {
			if _, ok := exp.SetString(s[pos+1:], 10); !ok {
				return scalar{}, false
			}
			s = s[:pos]
		}
		if pos := strings.IndexByte(s, '.'); pos >= 0 {
			exp.Sub(exp, big.NewInt(int64(len(s)-pos-1)))
			s = s[:pos] + s[pos+1:]
		}
		s = strings.TrimLeft(s, "0")
		if s == "" {
			return scalar{kind: 2, sign: 0}, true
		}
		exp.Add(exp, big.NewInt(int64(len(s))))
		return scalar{kind: 2, sign: sign, text: strings.TrimRight(s, "0"), order: exp}, true
	default:
		return scalar{}, false
	}
}

func compare(a, b scalar) int {
	if a.kind != b.kind {
		if a.kind < b.kind {
			return -1
		}
		return 1
	}
	if a.kind != 2 {
		return strings.Compare(a.text, b.text)
	}
	if a.sign != b.sign {
		if a.sign < b.sign {
			return -1
		}
		return 1
	}
	if a.sign == 0 {
		return 0
	}
	if c := a.order.Cmp(b.order); c != 0 {
		return c * a.sign
	}
	length := max(len(a.text), len(b.text))
	for i := 0; i < length; i++ {
		ac, bc := byte('0'), byte('0')
		if i < len(a.text) {
			ac = a.text[i]
		}
		if i < len(b.text) {
			bc = b.text[i]
		}
		if ac < bc {
			return -a.sign
		}
		if ac > bc {
			return a.sign
		}
	}
	return 0
}

func literal(value any) (scalar, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return scalar{}, fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var normalized any
	if err := d.Decode(&normalized); err != nil {
		return scalar{}, fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	s, ok := scalarOf(normalized)
	if !ok {
		return scalar{}, fmt.Errorf("%w: comparison requires a JSON scalar", ErrInvalidQuery)
	}
	return s, nil
}
