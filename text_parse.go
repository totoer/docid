package docid

import (
	"fmt"
	"strings"
)

type textLexeme struct {
	kind, value string
	quoted      bool
}
type textPiece struct {
	value  string
	prefix bool
}
type textPhrase struct {
	pieces  []textPiece
	initial bool
}
type textNode struct {
	kind        string
	left, right *textNode
	phrases     []textPhrase
	distance    int
	fields      []string
	exclude     bool
}

func textSyntax(message string) error { return fmt.Errorf("%w: FTS5 %s", ErrInvalidQuery, message) }

func (n *textNode) validateFields(fields []string) error {
	if n == nil {
		return nil
	}
	for _, name := range n.fields {
		found := false
		for _, field := range fields {
			if asciiLower(field) == asciiLower(name) {
				found = true
				break
			}
		}
		if !found {
			return textSyntax("unknown field " + name)
		}
	}
	if err := n.left.validateFields(fields); err != nil {
		return err
	}
	return n.right.validateFields(fields)
}

func textLex(query string) ([]textLexeme, error) {
	// SQLite MATCH treats input as a NUL-terminated expression.
	if end := strings.IndexByte(query, 0); end >= 0 {
		query = query[:end]
	}
	var tokens []textLexeme
	for i := 0; i < len(query); {
		c := query[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if strings.ContainsRune("(){}:,+*-^", rune(c)) {
			tokens = append(tokens, textLexeme{kind: string(c)})
			i++
			continue
		}
		if c == '"' {
			i++
			var s strings.Builder
			closed := false
			for i < len(query) {
				if query[i] == '"' {
					i++
					if i == len(query) || query[i] != '"' {
						closed = true
						break
					}
				}
				s.WriteByte(query[i])
				i++
			}
			if !closed {
				return nil, textSyntax("unterminated string")
			}
			tokens = append(tokens, textLexeme{kind: "string", value: s.String(), quoted: true})
			continue
		}
		start := i
		for i < len(query) && ftsBareword(query[i]) {
			i++
		}
		if start == i {
			return nil, textSyntax("invalid character")
		}
		s := query[start:i]
		kind := "string"
		if s == "AND" || s == "OR" || s == "NOT" {
			kind = s
		}
		tokens = append(tokens, textLexeme{kind: kind, value: s})
	}
	return append(tokens, textLexeme{kind: "eof"}), nil
}

type textParser struct {
	tokens []textLexeme
	pos    int
	err    error
}

func (p *textParser) peek() textLexeme { return p.tokens[p.pos] }
func (p *textParser) take(kind string) bool {
	if p.peek().kind == kind {
		p.pos++
		return true
	}
	return false
}
func (p *textParser) expect(kind string) textLexeme {
	t := p.peek()
	if !p.take(kind) && p.err == nil {
		p.err = textSyntax("expected " + kind)
	}
	return t
}
func parseTextQuery(query string) (*textNode, error) {
	tokens, err := textLex(query)
	if err != nil {
		return nil, err
	}
	p := textParser{tokens: tokens}
	n := p.expression(1)
	if p.peek().kind != "eof" && p.err == nil {
		p.err = textSyntax("unexpected token")
	}
	return n, p.err
}
func textPrecedence(kind string) int {
	switch kind {
	case "OR":
		return 1
	case "AND":
		return 2
	case "NOT":
		return 3
	}
	return 0
}
func (p *textParser) expression(min int) *textNode {
	left, near := p.primary()
	if near {
		for p.err == nil && (p.peek().kind == "string" || p.peek().kind == "-" || p.peek().kind == "{" || p.peek().kind == "^") {
			right, isNear := p.primary()
			if !isNear {
				p.err = textSyntax("implicit AND requires phrases or NEAR groups")
				break
			}
			left = &textNode{kind: "implicit", left: left, right: right}
		}
	}
	for p.err == nil {
		op := p.peek().kind
		prec := textPrecedence(op)
		if prec < min {
			break
		}
		p.pos++
		right := p.expression(prec + 1)
		left = &textNode{kind: op, left: left, right: right}
	}
	return left
}

func (p *textParser) primary() (*textNode, bool) {
	if p.err != nil {
		return nil, false
	}
	// A filter can precede a nearset or a parenthesized full expression.
	filter := p.peek().kind == "-" || p.peek().kind == "{"
	if p.peek().kind == "string" && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].kind == ":" {
		filter = true
	}
	if filter {
		n := &textNode{kind: "filter", exclude: p.take("-")}
		if p.take("{") {
			for p.peek().kind == "string" {
				n.fields = append(n.fields, p.peek().value)
				p.pos++
			}
			if len(n.fields) == 0 {
				p.err = textSyntax("empty field set")
			}
			p.expect("}")
		} else {
			n.fields = []string{p.expect("string").value}
		}
		p.expect(":")
		if p.take("(") {
			n.left = p.expression(1)
			p.expect(")")
			return n, false
		}
		n.left = p.nearset()
		return n, true
	}
	if p.take("(") {
		n := p.expression(1)
		p.expect(")")
		return n, false
	}
	return p.nearset(), true
}

func (p *textParser) nearset() *textNode {
	n := &textNode{kind: "near", distance: 10}
	if p.peek().kind == "string" && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].kind == "(" {
		name := p.peek()
		p.pos += 2
		if name.quoted || name.value != "NEAR" {
			p.err = textSyntax("expected NEAR")
			return n
		}
		n.phrases = append(n.phrases, p.phrase())
		for p.err == nil && p.peek().kind == "string" {
			n.phrases = append(n.phrases, p.phrase())
		}
		if p.take(",") {
			d := p.expect("string")
			n.distance = 0
			if d.quoted || len(d.value) == 0 {
				p.err = textSyntax("expected integer distance")
			}
			for _, c := range d.value {
				if c < '0' || c > '9' {
					p.err = textSyntax("expected integer distance")
					break
				}
				if n.distance < 214748363 {
					n.distance = n.distance*10 + int(c-'0')
				}
			}
		}
		p.expect(")")
		return n
	}
	initial := p.take("^")
	phrase := p.phrase()
	phrase.initial = initial
	n.phrases = []textPhrase{phrase}
	return n
}
func (p *textParser) phrase() textPhrase {
	var phrase textPhrase
	for {
		t := p.expect("string")
		phrase.pieces = append(phrase.pieces, textPiece{value: t.value, prefix: p.take("*")})
		if p.err != nil || !p.take("+") {
			break
		}
	}
	return phrase
}
