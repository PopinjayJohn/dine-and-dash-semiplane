package store_test

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// The benchmarks here are the two ADR 0009 says have to exist and does not
// measure: the cost of keeping the text twice, and the cost of reading a search.
// The ADR says "roughly double" and "kilobytes", and a sentence in a design
// document is not a number anybody can check a regression against.
//
// The corpus is grown past the spec's three-page fixture, because a benchmark
// over three pages measures the fixture. 400 pages is on the small side of a real
// campaign and keeps the whole thing under a second.

const benchPages = 400

// benchWords is the vocabulary the synthetic pages are made of. It is deliberately
// not a dictionary: every word appears in many pages and a few appear in one, so
// that BM25 has a spread to work with. A benchmark over a vocabulary of two words
// measures a dictionary.
var benchWords = []string{
	"toll", "bridge", "winter", "heist", "court", "umbral", "draconary",
	"harbour", "lantern", "quarry", "tithe", "fog", "reef", "silt", "moss",
	"cinder", "watch", "sable", "rivulet", "causeway", "almanac", "vestry",
}

// benchEntries is a campaign's worth of index entries, from a fixed seed so that
// two runs index the same bytes and a change in the numbers is a change in the
// code. The seed is fixed rather than random because a benchmark whose corpus
// moves is a benchmark whose numbers cannot be compared.
func benchEntries() []store.IndexEntry {
	random := rand.New(rand.NewSource(1)) //nolint:gosec // a reproducible benchmark fixture, not a key

	entries := make([]store.IndexEntry, benchPages)
	for i := range entries {
		body := strings.Join(benchWordsOf(random, 200), " ") + "\n"
		entries[i] = store.IndexEntry{
			PageID:     fmt.Sprintf("page-%03d", i),
			Title:      fmt.Sprintf("Place %03d", i),
			Aliases:    []string{fmt.Sprintf("the %dth place", i)},
			Tags:       []string{"location", fmt.Sprintf("region-%d", i%8)},
			Kind:       "location",
			BodyPublic: body,
		}
		// A tenth of the pages have a secret. That is generous for a real
		// campaign, and it keeps the private index from being trivially small.
		if i%10 == 0 {
			entries[i].SecretText = strings.Join(benchWordsOf(random, 12), " ")
		}
	}
	return entries
}

func benchWordsOf(random *rand.Rand, count int) []string {
	words := make([]string, count)
	for i := range words {
		words[i] = benchWords[random.Intn(len(benchWords))]
	}
	return words
}

// BenchmarkSearchIndex is the write cost, split into its two halves so the
// doubling ADR 0009 mentions is visible rather than asserted.
func BenchmarkSearchIndex(b *testing.B) {
	ctx := context.Background()
	s := newStoreTB(b)
	campaign := mustCreateCampaignTB(b, s)

	entries := benchEntries()

	// The page rows are written once, outside the loop: what is measured is the
	// index, not the row.
	for _, entry := range entries {
		if _, err := s.UpsertPage(ctx, benchPage(campaign.ID, entry), store.AsDM(campaign.ID)); err != nil {
			b.Fatalf("UpsertPage: %v", err)
		}
	}

	b.Run("write both indexes", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, entry := range entries {
				if err := s.ReplacePageIndex(context.Background(), entry); err != nil {
					b.Fatal(err)
				}
			}
		}
	})

	// The settled check is what the sync runs before it decides to write, so it
	// is on the hot path too — and its happy answer has to be fast, because it is
	// the answer for every page on every sync of an unchanged vault.
	b.Run("settled check, already settled", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, entry := range entries {
				settled, err := s.PageIndexMatches(context.Background(), entry)
				if err != nil {
					b.Fatal(err)
				}
				if !settled {
					b.Fatal("the fixture is not settled, so this is measuring a write")
				}
			}
		}
	})
}

// BenchmarkSearch is the read cost over a campaign of a plausible size, over the
// shapes a DM actually types: a word, two words, a phrase, a filter, and a word
// that matches nothing.
func BenchmarkSearch(b *testing.B) {
	ctx := context.Background()
	s := newStoreTB(b)
	campaign := mustCreateCampaignTB(b, s)

	entries := benchEntries()
	for _, entry := range entries {
		stored, err := s.UpsertPage(ctx, benchPage(campaign.ID, entry), store.AsDM(campaign.ID))
		if err != nil {
			b.Fatalf("UpsertPage: %v", err)
		}
		entry.PageID = stored.ID
		if err := s.ReplacePageIndex(ctx, entry); err != nil {
			b.Fatalf("ReplacePageIndex: %v", err)
		}
	}

	dm := domain.Principal{ID: "principal-dm", CampaignID: campaign.ID, Role: domain.RoleDM}

	queries := map[string]string{
		"one word":           "toll",
		"two words":          "toll bridge",
		"a phrase":           `"toll bridge"`,
		"a tag filter":       "tag:region-3",
		"a type filter":      "type:location",
		"a word everywhere":  "umbral",
		"a word in one page": "vestry",
		"a word nowhere":     "flumph",
	}

	for name, input := range queries {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := search.Run(ctx, s, campaign.ID, dm, input, search.DefaultLimit); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkFuse is the merge on its own, so a change in the two queries can be
// told apart from a change in the fusion. It reads no database at all, which is
// the property that makes it worth having.
func BenchmarkFuse(b *testing.B) {
	public := make([]search.Hit, search.MaxLimit)
	private := make([]search.Hit, search.MaxLimit/4)

	for i := range public {
		public[i] = search.Hit{PageID: fmt.Sprintf("id-%d", i), Rank: i + 1}
	}
	for i := range private {
		// Half of them are pages the public list also has, which is the case
		// fusion exists for and the one a benchmark that skipped it would miss.
		page := i
		if i%2 == 0 {
			page = i / 2
		}
		private[i] = search.Hit{PageID: fmt.Sprintf("id-%d", page), Rank: i + 1, FromSecrets: true}
	}

	b.ReportAllocs()
	for b.Loop() {
		if fused := search.Fuse(public, private); len(fused) == 0 {
			b.Fatal("the fusion returned nothing")
		}
	}
}

// benchPage is the page row behind one index entry.
func benchPage(campaignID string, entry store.IndexEntry) domain.Page {
	return domain.Page{
		CampaignID:  campaignID,
		Path:        "locations/" + entry.PageID,
		Title:       entry.Title,
		Type:        domain.PageTypeLocation,
		Visibility:  domain.VisibilityPlayers,
		Frontmatter: "title: " + entry.Title + "\n",
		Body:        entry.BodyPublic,
		ContentHash: "hash-of-" + entry.PageID,
	}
}
