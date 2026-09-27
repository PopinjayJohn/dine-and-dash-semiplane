package render

import (
	"strings"
	"testing"
)

// `body_public` is what the public search index is fed, so a word that is in a
// secret and nowhere else must not be in it. These are the cases that decide what
// goes on which side, and the last group is the one that is easy to get wrong: a
// *revealed* secret stays, because §9 says it is visible to everyone who can read
// the page and the public search only produces a hit for those.
//
// **A callout's title is in neither half.** It is an attribute on the node, not
// text in it — the same rule `SecretText` follows, and the reason a page's
// `[!SECRET]` headings do not turn up in a secret's excerpt. A heading inside the
// block is text, and is included; the block's own title is a label, and is not.
func TestPublicText(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body      string
		want      string
		wantCount int
	}{
		"a page with no secret is all of its text": {
			body: "A fortified town at the confluence of the [[Blackwater]].\n",
			want: "A fortified town at the confluence of the Blackwater.",
		},
		"a secret is replaced by a marker": {
			body: "A fortified town.\n" +
				"> [!SECRET] The toll-collector's real name\n" +
				"> Captain Vell is actually **Ilithya Marrow**.\n",
			want:      "A fortified town. […]",
			wantCount: 1,
		},
		"a revealed secret stays": {
			// §9: a revealed block is visible to everyone who can read the page, and
			// the public search's rows are filtered by the read predicate, so every
			// principal who can reach this text may read it.
			body: "A fortified town.\n" +
				"> [!SECRET]{.revealed} Everyone knows this\n" +
				"> The bridge was built in the ninth year.\n",
			want: "A fortified town. The bridge was built in the ninth year.",
		},
		"a revealed secret wrapping an unrevealed one leaves the inner out": {
			body: "> [!SECRET]{.revealed} The outer one\n" +
				"> Shown to everybody.\n" +
				">\n" +
				"> > [!SECRET] The inner one\n" +
				"> > The buried name.\n",
			want:      "Shown to everybody. […]",
			wantCount: 1,
		},
		"a blockquote with the shape of a secret and no closing bracket is treated as one": {
			body: "A town.\n" +
				"> [!SECRET The bracket is missing\n" +
				"> The mistyped secret.\n",
			want:      "A town. […]",
			wantCount: 1,
		},
		"an ordinary callout stays, without its title": {
			body: "> [!warning] The winter\n> The winter is not discussed further here.\n",
			want: "The winter is not discussed further here.",
		},
		"a wiki link contributes its label and not its path": {
			body: "See [[the toll collector|the collector]] for the price.\n",
			want: "See the collector for the price.",
		},
		"a code block in the prose stays": {
			body: "The key is:\n\n```\ntoll = 3d\n```\n",
			want: "The key is: toll = 3d",
		},
		"a page that is only a secret is only a marker": {
			body:      "> [!SECRET]\n> Everything on this page.\n",
			want:      "[…]",
			wantCount: 1,
		},
		"an empty body": {
			body: "",
			want: "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, count := PublicText(tt.body)
			if got != tt.want {
				t.Errorf("PublicText = %q, want %q", got, tt.want)
			}
			if count != tt.wantCount {
				t.Errorf("PublicText found %d secrets, want %d: %q", count, tt.wantCount, got)
			}
		})
	}
}

// The canary test for this function, and it is the one the whole split rests on:
// a word that appears only inside a secret must not be in the public text, and it
// must be in the private text. Either half failing is a disclosure — the first a
// searchable secret, the second a secret the DM has lost and cannot find.
func TestPublicTextHoldsNoSecretWord(t *testing.T) {
	t.Parallel()

	const canary = "IlithyaMarrowCanary"

	tests := map[string]struct {
		body        string
		publicHas   bool
		secretsHave bool
	}{
		"inside an unrevealed secret": {
			body:        "> [!SECRET]\n> The name is " + canary + ".\n",
			publicHas:   false,
			secretsHave: true,
		},
		"inside a malformed secret": {
			body:        "> [!SECRET The bracket is missing\n> The name is " + canary + ".\n",
			publicHas:   false,
			secretsHave: true,
		},
		"inside a revealed secret": {
			// Revealed means visible to everyone who can read the page, so it stays
			// in the public half. The private half does not need it.
			body:        "> [!SECRET]{.revealed}\n> The name is " + canary + ".\n",
			publicHas:   true,
			secretsHave: false,
		},
		"in ordinary prose": {
			body:        "A town whose toll-collector is " + canary + ".\n",
			publicHas:   true,
			secretsHave: false,
		},
		"in the secret's own title": {
			// The callout's title is an attribute, not text, so it is in neither
			// half. It is in the page's title column and in the rendered heading.
			body:        "> [!SECRET] The name is " + canary + "\n> Something else entirely.\n",
			publicHas:   false,
			secretsHave: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			public, _ := PublicText(tt.body)
			secrets, _ := SecretText(tt.body)

			if got := strings.Contains(public, canary); got != tt.publicHas {
				t.Errorf("the public text is %q, which holds the canary = %t, want %t",
					public, got, tt.publicHas)
			}
			if got := strings.Contains(secrets, canary); got != tt.secretsHave {
				t.Errorf("the private text is %q, which holds the canary = %t, want %t",
					secrets, got, tt.secretsHave)
			}
		})
	}
}

// The two halves come out of one walk, and they have to: two parsers for one
// grammar is how a secret ends up in both halves or in neither.
func TestPublicTextAndSecretTextPartitionThePage(t *testing.T) {
	t.Parallel()

	body := "A fortified town at the confluence of the [[Blackwater]].\n" +
		"\n" +
		"First paragraph of the toll.\n" +
		"\n" +
		"> [!SECRET] The toll-collector's real name\n" +
		"> Captain Vell is actually **Ilithya Marrow**.\n" +
		">\n" +
		"> - She engineered the collapse.\n" +
		"> - She answers to [[the-whisperer]].\n" +
		"\n" +
		"Last paragraph of the toll.\n" +
		"\n" +
		"> [!SECRET]{.revealed} Everyone knows this\n" +
		"> The bridge was built in the ninth year.\n"

	public, publicSecrets := PublicText(body)
	private, privateSecrets := SecretText(body)

	// The same number of secrets from the same walk.
	if publicSecrets != 1 || privateSecrets != 1 {
		t.Fatalf("PublicText found %d and SecretText found %d, want 1 each: the two "+
			"should be one walk of one tree", publicSecrets, privateSecrets)
	}

	// The prose is in the public half, once, and the secret is in neither.
	if strings.Contains(public, "Ilithya") {
		t.Errorf("the public text holds the secret: %q", public)
	}
	if strings.Contains(public, "engineered") {
		t.Errorf("the public text holds a list item from inside the secret: %q", public)
	}
	if !strings.Contains(public, "Last paragraph of the toll") {
		t.Errorf("the public text is missing the prose after the secret: %q", public)
	}
	if !strings.Contains(private, "engineered") {
		t.Errorf("the private text is missing a list item from the secret: %q", private)
	}

	// The revealed block is in the public half and not in the private one, so the
	// two do not both carry it.
	if !strings.Contains(public, "ninth year") {
		t.Errorf("the public text is missing the revealed secret: %q", public)
	}
	if strings.Contains(private, "ninth year") {
		t.Errorf("the private text holds the revealed secret as well: %q", private)
	}
}

// The marker tokenises to nothing, which is the point of it: a page's public text
// is a body with holes in it, and a hole must not be a word somebody can search
// for.
func TestTheMarkerIsNotAWord(t *testing.T) {
	t.Parallel()

	got, count := PublicText("A town.\n\n> [!SECRET]\n> A name.\n")
	if count != 1 {
		t.Fatalf("found %d secrets, want 1", count)
	}
	if !strings.Contains(got, PublicSecretMarker) {
		t.Fatalf("the marker is missing from %q", got)
	}

	// Nothing in the marker is a letter or a digit, so the tokenizer discards all
	// of it. A marker that contributed a token would appear in the relevance of
	// every page with a secret, and a search for that token would return them all.
	for _, r := range PublicSecretMarker {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			t.Errorf("the marker %q contributes the token %q to the index", PublicSecretMarker, string(r))
		}
	}
}
