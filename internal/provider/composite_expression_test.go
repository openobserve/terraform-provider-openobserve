package provider

import "testing"

// Operands must be brace-wrapped KSUIDs. Using realistic ones keeps these tests
// honest about what the server will actually accept.
const (
	childA = "2fXkZ8QlmNbYcV1pR3sTaAaAaAa"
	childB = "2fXkZ8QlmNbYcV1pR3sTbBbBbBb"
	childC = "2fXkZ8QlmNbYcV1pR3sTcCcCcCc"
)

func TestCanonicalCompositeExpressionMatchesServerForm(t *testing.T) {
	// The server persists a fully parenthesized rewrite. These expectations are
	// the shapes it stores, so a mismatch here is drift in production.
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "and",
			input: "{" + childA + "} && {" + childB + "}",
			want:  "({" + childA + "} && {" + childB + "})",
		},
		{
			name:  "already canonical round-trips",
			input: "({" + childA + "} && {" + childB + "})",
			want:  "({" + childA + "} && {" + childB + "})",
		},
		{
			name:  "and binds tighter than or",
			input: "{" + childA + "} || {" + childB + "} && {" + childC + "}",
			want:  "({" + childA + "} || ({" + childB + "} && {" + childC + "}))",
		},
		{
			name:  "explicit parens override precedence",
			input: "({" + childA + "} || {" + childB + "}) && {" + childC + "}",
			want:  "(({" + childA + "} || {" + childB + "}) && {" + childC + "})",
		},
		{
			name:  "and is left associative",
			input: "{" + childA + "} && {" + childB + "} && {" + childC + "}",
			want:  "(({" + childA + "} && {" + childB + "}) && {" + childC + "})",
		},
		{
			name:  "not binds tightest",
			input: "!{" + childA + "} && {" + childB + "}",
			want:  "((!{" + childA + "}) && {" + childB + "})",
		},
		{
			name:  "whitespace is insignificant",
			input: "  {" + childA + "}\t&&\n{" + childB + "}  ",
			want:  "({" + childA + "} && {" + childB + "})",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := validateCompositeExpression(tc.input)
			if err != nil {
				t.Fatalf("validateCompositeExpression(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("canonical form = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCompositeReferencesFollowExpressionOrder(t *testing.T) {
	_, refs, err := validateCompositeExpression("{" + childC + "} && (!{" + childA + "} || {" + childB + "})")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{childC, childA, childB}
	if len(refs) != len(want) {
		t.Fatalf("got %d references, want %d", len(refs), len(want))
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("reference %d = %q, want %q", i, refs[i], want[i])
		}
	}
}

func TestValidateCompositeExpressionRejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"single child is not a composite", "{" + childA + "}"},
		{"duplicate operand", "{" + childA + "} && {" + childA + "}"},
		{"single ampersand", "{" + childA + "} & {" + childB + "}"},
		{"unclosed paren", "({" + childA + "} && {" + childB + "}"},
		{"unclosed brace", "{" + childA + " && {" + childB + "}"},
		{"empty operand", "{} && {" + childB + "}"},
		{"bare identifier", childA + " && {" + childB + "}"},
		{"trailing operator", "{" + childA + "} &&"},
		{"trailing tokens", "{" + childA + "} && {" + childB + "}) "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := validateCompositeExpression(tc.input); err == nil {
				t.Errorf("validateCompositeExpression(%q) succeeded, want an error", tc.input)
			}
		})
	}
}

func TestValidateCompositeExpressionEnforcesChildCount(t *testing.T) {
	// Eleven distinct children: one past the server's cap of ten.
	expr := ""
	for i := 0; i < 11; i++ {
		if i > 0 {
			expr += " || "
		}
		expr += "{2fXkZ8QlmNbYcV1pR3sT" + string(rune('a'+i)) + "AaAaAa}"
	}
	if _, _, err := validateCompositeExpression(expr); err == nil {
		t.Fatal("expected an error for 11 children, got none")
	}
}

func TestCompositeExpressionsEquivalent(t *testing.T) {
	configured := "{" + childA + "} && {" + childB + "}"
	stored := "({" + childA + "} && {" + childB + "})"

	if !compositeExpressionsEquivalent(configured, stored) {
		t.Error("a configured expression and its canonical form must compare equal, or every plan reports drift")
	}
	if compositeExpressionsEquivalent(configured, "({"+childA+"} || {"+childB+"})") {
		t.Error("&& and || must not compare equal")
	}
	// An unparseable stored value has to surface as a diff rather than being
	// quietly accepted as equivalent.
	if compositeExpressionsEquivalent(configured, "not an expression") {
		t.Error("unparseable input must not compare equal")
	}
}
