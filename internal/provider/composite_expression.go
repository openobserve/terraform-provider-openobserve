package provider

import (
	"fmt"
	"strings"
	"unicode"
)

// A composite alert's boolean expression, parsed here rather than only on the
// server, for two reasons.
//
// The server stores the *canonical* form: it reparses whatever it is sent and
// writes back a fully parenthesized string, so `{a} && {b}` is read back as
// `({a} && {b})`. Comparing the two spellings directly would report drift on
// every plan for a configuration that never changed. Canonicalizing both sides
// before comparing settles that.
//
// It also moves syntax errors, duplicate operands and child-count violations
// from an apply-time 400 to a `plan` diagnostic pointing at the offending line.
//
// The grammar mirrors the server's exactly, including precedence:
//
//	or    := and ( "||" and )*
//	and   := unary ( "&&" unary )*
//	unary := "!" unary | atom
//	atom  := "{" alert_id "}" | "(" or ")"
//
// `&&` binds tighter than `||`, and `!` tighter than both. Diverging from the
// server here would silently change what a composite means, with no error
// raised anywhere, so this is deliberately a transcription and not a rewrite.

type compositeTokenKind int

const (
	compositeTokIdent compositeTokenKind = iota
	compositeTokAnd
	compositeTokOr
	compositeTokNot
	compositeTokLParen
	compositeTokRParen
)

type compositeToken struct {
	kind compositeTokenKind
	text string
}

// compositeExpr is a parsed expression tree.
type compositeExpr struct {
	kind  compositeTokenKind // Ident, And, Or, or Not
	child string             // set when kind is Ident
	left  *compositeExpr
	right *compositeExpr
}

func compositeTokenize(input string) ([]compositeToken, error) {
	if len(input) > CompositeMaxExpressionLen {
		return nil, fmt.Errorf("expression exceeds %d bytes", CompositeMaxExpressionLen)
	}

	runes := []rune(input)
	var out []compositeToken
	for i := 0; i < len(runes); {
		c := runes[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '(':
			out = append(out, compositeToken{kind: compositeTokLParen})
			i++
		case c == ')':
			out = append(out, compositeToken{kind: compositeTokRParen})
			i++
		case c == '!':
			out = append(out, compositeToken{kind: compositeTokNot})
			i++
		case c == '&' || c == '|':
			if i+1 >= len(runes) || runes[i+1] != c {
				return nil, fmt.Errorf("expected `%c%c`", c, c)
			}
			kind := compositeTokAnd
			if c == '|' {
				kind = compositeTokOr
			}
			out = append(out, compositeToken{kind: kind})
			i += 2
		case c == '{':
			end := -1
			for j := i + 1; j < len(runes); j++ {
				if runes[j] == '}' {
					end = j
					break
				}
			}
			if end < 0 {
				return nil, fmt.Errorf("unclosed `{`")
			}
			raw := string(runes[i+1 : end])
			if raw == "" || strings.ContainsAny(raw, "{}") {
				return nil, fmt.Errorf("operands must be brace-wrapped alert IDs")
			}
			out = append(out, compositeToken{kind: compositeTokIdent, text: raw})
			i = end + 1
		default:
			return nil, fmt.Errorf("unexpected character `%c`", c)
		}
	}
	return out, nil
}

type compositeParser struct {
	tokens []compositeToken
	pos    int
}

func (p *compositeParser) peek() *compositeToken {
	if p.pos < len(p.tokens) {
		return &p.tokens[p.pos]
	}
	return nil
}

func (p *compositeParser) parseOr() (*compositeExpr, error) {
	lhs, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != compositeTokOr {
			return lhs, nil
		}
		p.pos++
		rhs, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		lhs = &compositeExpr{kind: compositeTokOr, left: lhs, right: rhs}
	}
}

func (p *compositeParser) parseAnd() (*compositeExpr, error) {
	lhs, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != compositeTokAnd {
			return lhs, nil
		}
		p.pos++
		rhs, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		lhs = &compositeExpr{kind: compositeTokAnd, left: lhs, right: rhs}
	}
}

func (p *compositeParser) parseUnary() (*compositeExpr, error) {
	if t := p.peek(); t != nil && t.kind == compositeTokNot {
		p.pos++
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &compositeExpr{kind: compositeTokNot, left: inner}, nil
	}
	return p.parseAtom()
}

func (p *compositeParser) parseAtom() (*compositeExpr, error) {
	t := p.peek()
	if t == nil {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	switch t.kind {
	case compositeTokIdent:
		p.pos++
		return &compositeExpr{kind: compositeTokIdent, child: t.text}, nil
	case compositeTokLParen:
		p.pos++
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if next := p.peek(); next == nil || next.kind != compositeTokRParen {
			return nil, fmt.Errorf("unclosed `(`")
		}
		p.pos++
		return inner, nil
	default:
		return nil, fmt.Errorf("unexpected token in expression")
	}
}

// parseCompositeExpression parses an expression into a tree.
func parseCompositeExpression(input string) (*compositeExpr, error) {
	tokens, err := compositeTokenize(input)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty expression")
	}
	p := &compositeParser{tokens: tokens}
	expr, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.tokens) {
		return nil, fmt.Errorf("trailing tokens after expression")
	}
	return expr, nil
}

// canonicalCompositeExpression renders the fully parenthesized form the server
// persists, so a stored expression can be compared against a configured one.
func canonicalCompositeExpression(expr *compositeExpr) string {
	switch expr.kind {
	case compositeTokIdent:
		return "{" + expr.child + "}"
	case compositeTokAnd:
		return "(" + canonicalCompositeExpression(expr.left) + " && " + canonicalCompositeExpression(expr.right) + ")"
	case compositeTokOr:
		return "(" + canonicalCompositeExpression(expr.left) + " || " + canonicalCompositeExpression(expr.right) + ")"
	case compositeTokNot:
		return "(!" + canonicalCompositeExpression(expr.left) + ")"
	default:
		return ""
	}
}

// compositeReferences collects operand IDs in expression order, rejecting a
// repeated operand. The server refuses duplicates rather than deduplicating
// them, because `{a} && {a}` almost always means a second alert was meant.
func compositeReferences(expr *compositeExpr) ([]string, error) {
	seen := make(map[string]struct{})
	var out []string
	var walk func(*compositeExpr) error
	walk = func(e *compositeExpr) error {
		switch e.kind {
		case compositeTokIdent:
			if _, dup := seen[e.child]; dup {
				return fmt.Errorf("child `%s` referenced more than once", e.child)
			}
			seen[e.child] = struct{}{}
			out = append(out, e.child)
		case compositeTokAnd, compositeTokOr:
			if err := walk(e.left); err != nil {
				return err
			}
			return walk(e.right)
		case compositeTokNot:
			return walk(e.left)
		}
		return nil
	}
	if err := walk(expr); err != nil {
		return nil, err
	}
	return out, nil
}

// validateCompositeExpression parses an expression and checks everything the
// provider can check without talking to the server. It returns the canonical
// form and the referenced child IDs.
//
// It deliberately does not check whether the children exist, are eligible, or
// would form a cycle: those need the organization's alert graph, which only the
// server holds.
func validateCompositeExpression(input string) (string, []string, error) {
	expr, err := parseCompositeExpression(input)
	if err != nil {
		return "", nil, err
	}
	refs, err := compositeReferences(expr)
	if err != nil {
		return "", nil, err
	}
	if len(refs) < CompositeMinChildren {
		return "", nil, fmt.Errorf(
			"a composite needs at least %d children, got %d; a composite of one is just the child",
			CompositeMinChildren, len(refs),
		)
	}
	if len(refs) > CompositeMaxChildren {
		return "", nil, fmt.Errorf("a composite allows at most %d children, got %d", CompositeMaxChildren, len(refs))
	}
	return canonicalCompositeExpression(expr), refs, nil
}

// compositeExpressionsEquivalent reports whether two spellings denote the same
// expression. Unparseable input is compared literally, so a malformed stored
// value still surfaces as a diff instead of being silently accepted.
func compositeExpressionsEquivalent(a, b string) bool {
	if a == b {
		return true
	}
	left, err := parseCompositeExpression(a)
	if err != nil {
		return false
	}
	right, err := parseCompositeExpression(b)
	if err != nil {
		return false
	}
	return canonicalCompositeExpression(left) == canonicalCompositeExpression(right)
}
