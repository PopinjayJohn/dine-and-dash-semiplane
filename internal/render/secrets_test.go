package render

import (
	"strings"
	"testing"
)

// These cases are about which side of the split each piece of a page's text goes
// on, because that is what the private search index is fed (ADR 0009) and a
// secret on the wrong side is either unfindable for the DM or findable by
// somebody else.
func TestSecretText(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body      string
		want      string
		wantCount int
	}{
		"a page with no secret": {
			body: "A fortified town at the confluence of the [[Blackwater]].\n",
		},
		"an ordinary callout is not a secret": {
			body: "> [!warning] The winter\n> The winter is not discussed further here.\n",
		},
		"a secret": {
			body: "A fortified town.\n" +
				"> [!SECRET] The toll-collector's real name\n" +
				"> Captain Vell is actually **Ilithya Marrow**.\n",
			want:      "Captain Vell is actually Ilithya Marrow.",
			wantCount: 1,
		},
		// The words a search runs against. This is the case the private index
		// exists for, and it is the reason the join is a space and not nothing.
		"a secret split across inline nodes": {
			body:      "> [!SECRET]\n> The name is Ilithya Marrow.\n",
			want:      "The name is Ilithya Marrow.",
			wantCount: 1,
		},
		"two secrets": {
			body: "> [!SECRET] One\n> The first name.\n" +
				"\nSome prose.\n" +
				"\n> [!SECRET] Two\n> The second name.\n",
			want:      "The first name. The second name.",
			wantCount: 2,
		},
		"a revealed secret is not secret text": {
			body: "> [!SECRET]{.revealed} Shown already\n> Everyone knows this one.\n",
		},
		"a revealed secret wrapping an unrevealed one finds the inner": {
			// The outer block is public, so the walk goes into it — and the inner
			// block is not revealed, so its text is secret after all.
			body: "> [!SECRET]{.revealed} The outer one\n" +
				"> Shown to everybody.\n" +
				">\n" +
				"> > [!SECRET] The inner one\n" +
				"> > The buried name.\n",
			want:      "The buried name.",
			wantCount: 1,
		},
		"a secret inside a list item": {
			body: "- Some item\n" +
				"  > [!SECRET] Buried in a list\n" +
				"  > The listed secret.\n",
			want:      "The listed secret.",
			wantCount: 1,
		},
		"a secret inside another callout": {
			body: "> [!note] A note\n" +
				"> With something in it.\n" +
				">\n" +
				"> > [!SECRET] Inside\n" +
				"> > The nested secret.\n",
			want:      "The nested secret.",
			wantCount: 1,
		},
		"a secret inside a secret is counted once": {
			// The outer one already carries the inner one's text, so counting it
			// again would make a page that nests them rank differently from a
			// page that does not.
			body: "> [!SECRET] Outer\n" +
				"> The outer text.\n" +
				">\n" +
				"> > [!SECRET] Inner\n" +
				"> > The inner text.\n",
			want:      "The outer text. The inner text.",
			wantCount: 1,
		},
		// The callout's own title is an attribute, not text, so it is not in the
		// string. It is in the page's title column and a DM searching for it is
		// searching for a heading, which the public half carries.
		"a secret with a code block in it": {
			body: "> [!SECRET] Credentials\n" +
				"> ```\n" +
				"> key = aVeryLongSecretKey\n" +
				"> ```\n",
			want:      "key = aVeryLongSecretKey",
			wantCount: 1,
		},
		"a code span in a secret": {
			body:      "> [!SECRET]\n> The key is `Ilithya` Marrow.\n",
			want:      "The key is Ilithya Marrow.",
			wantCount: 1,
		},
		"a secret with a fold marker": {
			body:      "> [!SECRET]- Folded open\n> Folded text.\n",
			want:      "Folded text.",
			wantCount: 1,
		},
		"a blockquote with the shape of a secret and no closing bracket": {
			// The fail-closed case. The parser could not read it, and the only safe
			// reading is that it is a secret — so its text is indexed, because a
			// secret that cannot be found is a secret the DM has lost.
			body: "> [!SECRET The bracket is missing\n> The mistyped secret.\n",
			// The whole blockquote is the secret, header line included: the
			// parser could not tell where the header ended, so there was no header
			// to leave out.
			want:      "[!SECRET The bracket is missing The mistyped secret.",
			wantCount: 1,
		},
		"a blockquote that is only prose is not a secret": {
			body: "> Just a quotation about a toll-collector.\n",
		},
		"a table row is not a callout": {
			body: "| thing | note |\n| --- | --- |\n| toll | paid |\n",
		},
		"an empty body": {
			body: "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, count := SecretText(tt.body)
			if count != tt.wantCount {
				t.Errorf("SecretText found %d secrets, want %d: the text was %q", count, tt.wantCount, got)
			}
			if got != tt.want {
				t.Errorf("SecretText = %q, want %q", got, tt.want)
			}
		})
	}
}

// The canary test for this function: a secret's text goes into the string, and
// nothing else does. The prohibition runs both ways — a secret that is not
// extracted is a secret the DM cannot find, and ordinary prose that is extracted
// is prose in the private index, which is the index whose excerpt is secret text.
func TestSecretTextHoldsOnlySecrets(t *testing.T) {
	t.Parallel()

	const canary = "IlithyaMarrowCanary"

	tests := map[string]struct {
		body     string
		inSecret bool
	}{
		"inside a secret": {
			body:     "> [!SECRET]\n> The name is " + canary + ".\n",
			inSecret: true,
		},
		"inside a malformed secret": {
			body:     "> [!SECRET The bracket is missing\n> The name is " + canary + ".\n",
			inSecret: true,
		},
		"inside a revealed secret": {
			body: "> [!SECRET]{.revealed}\n> The name is " + canary + ".\n",
		},
		"in ordinary prose": {
			body: "A town whose toll-collector is " + canary + ".\n",
		},
		"in a title line": {
			body: "> [!SECRET] The name is " + canary + "\n> Something else.\n",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, _ := SecretText(tt.body)
			contains := strings.Contains(got, canary)
			if contains != tt.inSecret {
				t.Errorf("SecretText gave %q, which contains the canary = %t, want %t",
					got, contains, tt.inSecret)
			}
		})
	}
}

// A soft-wrapped secret. Every DM wraps their notes, and every wrapped line
// arrives as two text nodes with the newline and the `> ` marker between them
// and no node to say so — so the boundary has to be recovered from the source.
// Without that, a two-line secret is indexed as one run-on line and none of its
// phrases match.
func TestSecretTextKeepsSoftWrappedLinesApart(t *testing.T) {
	t.Parallel()

	const body = "> [!SECRET] The long name\n" +
		"> Captain Vell is actually Ilithya Marrow, and has been since the\n" +
		"> siege of the Umbral Court.\n"

	got, count := SecretText(body)
	if count != 1 {
		t.Fatalf("SecretText found %d secrets, want 1", count)
	}

	for _, phrase := range []string{
		"Ilithya Marrow, and has been since the siege of the Umbral Court.",
		"since the siege of the Umbral Court",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("SecretText gave %q, which does not contain the phrase %q: a wrapped line was run into the next one",
				got, phrase)
		}
	}
}
