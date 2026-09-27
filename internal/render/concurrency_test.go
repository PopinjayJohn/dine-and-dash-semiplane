package render

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// A render is one call per HTTP request from M8 onwards, and a wiki has a DM and a
// handful of players, so concurrent use is the normal case rather than the
// exceptional one. One `goldmark.Markdown` is shared by every render, every link
// resolution and every secret extraction, and whether that is safe is a fact about
// goldmark's internals that this project concluded by reading them -- see the
// comment in parse.go, which is deliberately careful about what was and was not
// established.
//
// So this test asserts the property that actually matters and that this project is
// responsible for: **a secret is never in the public text, and a render is never
// wrong about one, when eight goroutines are doing it at once.** That holds with
// or without the lock today, and a future change to goldmark, to the extensions,
// or to the collector could break it without breaking any other test in the
// package.
//
// The two bodies differ only in whether a backslash precedes a space, which is
// the sort of thing a shared parse context would confuse.

func TestParsingIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	// Two pages whose parse differs only in whether a backslash precedes a space,
	// which is the field goldmark keeps on the parser rather than in a parse
	// context. If the two interleave, one gets the other's answer.
	escaped := "A town where the name is written with a space: Aria\\ Wren.\n" +
		"\n" +
		"> [!SECRET] Not to be found\n" +
		"> Captain Vell is actually Ilithya Marrow.\n"
	plain := "A town where the name is written with a space: Aria Wren.\n" +
		"\n" +
		"> [!SECRET] Not to be found\n" +
		"> Captain Vell is actually Ilithya Marrow.\n"

	const goroutines = 8
	const each = 25

	var wg sync.WaitGroup
	failures := make(chan string, goroutines*each)

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				// The private half: a word that is only in a secret must be here
				// and not in the public one, and the two must always agree about
				// which is which.
				private, count := SecretText(escaped)
				if count != 1 || !strings.Contains(private, "Ilithya") {
					failures <- "SecretText: " + private
					return
				}
				if public, _ := PublicText(escaped); strings.Contains(public, "Ilithya") {
					failures <- "PublicText leaked the secret: " + public
					return
				}
				if public, _ := PublicText(plain); strings.Contains(public, "Ilithya") {
					failures <- "PublicText leaked the secret from the plain body: " + public
					return
				}
				if !strings.Contains(escaped, "Aria\\ Wren") {
					failures <- "the fixture lost its backslash"
					return
				}
			}
		}()
	}

	wg.Wait()
	close(failures)

	for failure := range failures {
		t.Error(failure)
	}
}

// And the same for the renderer, because a render is the call M8 makes per request
// and it is the one that turns a page into bytes a browser will believe.
//
// Each goroutine gets its own renderer, because a shared one would also be a
// shared cache and the cache is a separate question. What is under test here is
// the parser.
func TestRenderingIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	const canary = "IlithyaMarrowCanary"

	body := "A fortified town.\\ The bridge holds.\n" +
		"\n" +
		"> [!SECRET] The toll-collector's real name\n" +
		"> Captain Vell is actually **" + canary + "**.\n"

	page := Page{Path: "locations/rivergate", Body: body, ContentHash: "hash-of-rivergate"}

	var wg sync.WaitGroup
	failures := make(chan string, 8*10)

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			renderer := New()
			for range 10 {
				for _, decision := range []Decision{{}, {CanSeeSecrets: true}} {
					result, err := renderer.Render(context.Background(), page, decision)
					if err != nil {
						failures <- "Render: " + err.Error()
						return
					}

					leaked := strings.Contains(result.HTML, canary) != decision.CanSeeSecrets
					if leaked {
						failures <- "the render disagreed with the decision about the secret: " + result.HTML
						return
					}
				}
			}
		}()
	}

	wg.Wait()
	close(failures)

	for failure := range failures {
		t.Error(failure)
	}
}
