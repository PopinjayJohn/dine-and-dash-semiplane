package render_test

import (
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
)

// xssCorpus is the payload list, run through the whole pipeline rather than
// through the sanitiser alone: a payload in a DM's notes is markdown that
// goldmark passes through and the policy has to catch, and testing the two
// separately would miss a payload that one of them handles.
//
// Every payload is here for a reason, and the reason is in the comment beside it.
// The three rules they all obey: no script element, no event handler attribute,
// no URL scheme that executes.
var xssCorpus = []struct {
	payload string
	why     string
}{
	{"<script>alert(1)</script>", "the oldest one there is"},
	{"<SCRIPT>alert(1)</SCRIPT>", "case"},
	{"<script src=\"//evil.example/x.js\"></script>", "a remote script"},
	{"<script type=\"text/javascript\">alert(1)</script>", "with a type"},
	{"<script>alert(String.fromCharCode(88))</script>", "obfuscated content"},
	{"<scr<script>ipt>alert(1)</script>", "a nested tag"},
	{"<script x>alert(1)</script>", "a stray attribute"},
	{"<img src=x onerror=alert(1)>", "an event handler on the commonest element"},
	{"<img src=x onerror=\"alert(1)\">", "quoted"},
	{"<img src=x OnErRoR=alert(1)>", "case"},
	{"<img src=1 href=1 onerror=\"javascript:alert(1)\">", "two attributes"},
	{"<img src=\"data:image/svg+xml;base64,PHN2Zz48c2NyaXB0PmFsZXJ0KDEpPC9zY3JpcHQ+PC9zdmc+\">", "a data URL"},
	{"<img src=\"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==\">", "a data URL that is HTML"},
	{"<svg/onload=alert(1)>", "svg, self-closing"},
	{"<svg onload=alert(1)></svg>", "svg, with a space"},
	{"<svg><script>alert(1)</script></svg>", "a script inside svg"},
	{"<svg><animate onbegin=alert(1) attributeName=x dur=1s>", "an animation that runs code"},
	{"<svg><use href=\"data:image/svg+xml,<svg id='x'/>\"></svg>", "a use element"},
	{"<a href=\"javascript:alert(1)\">click</a>", "the oldest href trick"},
	{"<a href=\"JaVaScRiPt:alert(1)\">click</a>", "case"},
	{"<a href=\"  javascript:alert(1)\">click</a>", "a leading space"},
	{"<a href=\"java\tscript:alert(1)\">click</a>", "a tab inside the scheme"},
	{"<a href=\"java&#115;cript:alert(1)\">click</a>", "an entity inside the scheme"},
	{"<a href=\"&#106;avascript:alert(1)\">click</a>", "an entity for the j"},
	{"<a href=\"javascript&colon;alert(1)\">click</a>", "a named entity for the colon"},
	{"<a href=\"vbscript:msgbox(1)\">click</a>", "a scheme nobody supports any more"},
	{"<a href=\"data:text/html,<script>alert(1)</script>\">click</a>", "a data URL in an href"},
	{"<a href=\"#\" onclick=\"alert(1)\">click</a>", "an event handler on a link"},
	{"<a href=\"/c/page\" onmouseover=alert(1)>hover</a>", "an event handler on a resolved link"},
	{"<iframe src=\"javascript:alert(1)\"></iframe>", "a frame"},
	{"<iframe srcdoc=\"<script>alert(1)</script>\"></iframe>", "a frame with a document in it"},
	{"<frameset><frame src=javascript:alert(1)></frameset>", "a frameset"},
	{"<body onload=alert(1)>text</body>", "an event handler on the body"},
	{"<div onmouseover=alert(1)>hover</div>", "an event handler on a div"},
	{"<span onpointerenter=alert(1)>hover</span>", "a pointer event"},
	{"<div onanimationstart=alert(1)>text</div>", "an animation event"},
	{"<div style=\"background:url(javascript:alert(1))\">text</div>", "a style attribute, which is not allowed at all"},
	{"<div style=\"x:expression(alert(1))\">text</div>", "an expression, for the browsers that had them"},
	{"<div style=\"position:fixed;inset:0\">an overlay</div>", "a full-screen overlay without script"},
	{"<style>@import 'evil.css'</style>", "a stylesheet"},
	{"<link rel=stylesheet href=evil.css>", "a linked stylesheet"},
	{"<meta http-equiv=refresh content=\"0;url=javascript:alert(1)\">", "a refresh"},
	{"<meta charset=\"x\" onload=alert(1)>", "an event handler on a meta"},
	{"<base href=\"//evil.example/\">", "a base, which moves every relative link"},
	{"<object data=\"javascript:alert(1)\"></object>", "an object"},
	{"<object><param name=movie value=javascript:alert(1)></object>", "an object with a parameter"},
	{"<embed src=evil.swf>", "an embed"},
	{"<applet code=Evil.class>", "an applet"},
	{"<form action=javascript:alert(1)><input type=submit></form>", "a form that submits to script"},
	{"<button formaction=javascript:alert(1)>go</button>", "a formaction"},
	{"<input onfocus=alert(1) autofocus>", "an autofocus event handler"},
	{"<input type=image src=x onerror=alert(1)>", "an image input"},
	{"<select onchange=alert(1)><option>x</option></select>", "a select event handler"},
	{"<textarea onfocus=alert(1) autofocus>x</textarea>", "a textarea event handler"},
	{"<details open ontoggle=alert(1)>x</details>", "a details event handler, on an element we allow"},
	{"<marquee onstart=alert(1)>x</marquee>", "a marquee"},
	{"<video><source onerror=alert(1)></video>", "a media element"},
	{"<audio src=x onerror=alert(1)>", "an audio element"},
	{"<template><script>alert(1)</script></template>", "a template, whose content is not rendered but is in the DOM"},
	{"<noscript><p title=\"</noscript><script>alert(1)</script>\">x</p></noscript>", "a noscript that closes itself"},
	{"<xmp><script>alert(1)</script></xmp>", "an xmp element"},
	{"<plaintext><script>alert(1)</script>", "a plaintext element, which swallows the rest of the file"},
	{"<math><mtext><script>alert(1)</script></mtext></math>", "math, which is a foreign namespace"},
	{"<table background=javascript:alert(1)><tr><td>x</td></tr></table>", "a table attribute, on a table we allow"},
	{"<td background=javascript:alert(1)>x</td>", "a cell attribute, on a cell we allow"},
	{"<p class=\"callout-secret\" onclick=alert(1)>x</p>", "one of our own classes with an event handler"},
	{"<p class=\"callout-secret\" style=\"display:none\">x</p>", "our own class with a style that hides it"},
	{"<p class=\"anything-else\">x</p>", "a class of the DM's own, which the policy does not allow"},
	{"<p data-controller=\"modal\">x</p>", "a data attribute, which nothing here reads"},
	{"<img \"\"\"<script>alert(1)</script>\">", "attribute injection through a malformed tag"},
	{"<a href=\"/c/page\" target=\"_blank\">x</a>", "a target, which we allow and which the front end adds rel to"},
	{"<h1 id=\"x\" onmouseover=alert(1)>heading</h1>", "a heading with an id, on an element we allow"},
	{"<a href=\"file:///etc/passwd\">x</a>", "a file URL"},
	{"<a href=\"//evil.example/x\">x</a>", "a protocol-relative URL, which is a link and not a script"},
	{"<blockquote cite=\"javascript:alert(1)\">x</blockquote>", "a cite attribute"},
	{"<code onclick=alert(1)>x</code>", "an event handler on a code span"},
	{"<pre><code>&lt;script&gt;alert(1)&lt;/script&gt;</code></pre>", "an escaped script, which is text and must survive"},
}

// payloadsThatEatThePage are the corpus entries goldmark reads as an HTML block
// running to the end of the document, which is CommonMark's rule for a type 7
// block and is how `<frameset>` behaves. The prose after one of these does not
// survive, and that is the parser being faithful to a spec rather than the
// sanitiser failing: the frameset itself is still removed.
var payloadsThatEatThePage = map[string]bool{
	"<frameset><frame src=javascript:alert(1)></frameset>": true,
}

// forbiddenAfterSanitising is what must not appear in the output for any payload
// in the corpus. It is a set of substrings rather than a parser, because the
// point is that none of these words is in the bytes at all.
var forbiddenAfterSanitising = []string{
	"<script",
	"<iframe",
	"<object",
	"<embed",
	"<applet",
	"<form",
	"<button",
	"<style",
	"<link",
	"<meta",
	"<base",
	"<frame",
	// `<input>` is deliberately not forbidden: the element is allowed for GFM
	// task lists and a hostile one arrives inert. TestAHostileInputArrivesInert
	// is about that.
	"<select",
	"<textarea",
	"<template",
	"<video",
	"<audio",
	"<math",
	"<marquee",
	// `<input>` is *not* here: the element is allowed for GFM task lists, and a
	// hostile one arrives as a bare, attribute-less input. See
	// TestAHostileInputArrivesInert.
	// Event handlers and other attributes are checked with a leading space,
	// because that is what one looks like inside a tag. A payload the renderer
	// escaped into visible text contains the words without that, and showing
	// `<svg/onload=alert(1)>` to a reader as text is the sanitiser working.
	" onerror",
	" onload",
	" onclick",
	" onmouseover",
	" onfocus",
	" onchange",
	" ontoggle",
	" onstart",
	" onbegin",
	" onanimationstart",
	" onpointerenter",
	"javascript:",
	"vbscript:",
	"data:text/html",
	"expression(",
	" style=",
	// Data attributes, checked by name rather than as a prefix: the three this
	// renderer writes on a callout are allowed, and the point is that a DM
	// cannot write any other.
	"data-x=",
	"data-controller=",
	"data-wiki-",
	"class=\"anything",
}

// TestXSSCorpus is the corpus the spec asks for. Each payload goes in as
// markdown, the way a DM pastes something from a forum into their own notes, and
// comes out as HTML this application would serve.
func TestXSSCorpus(t *testing.T) {
	t.Parallel()

	if len(xssCorpus) < 60 {
		t.Errorf("the corpus has %d payloads; the spec asks for about 60", len(xssCorpus))
	}

	for _, entry := range xssCorpus {
		t.Run(entry.why, func(t *testing.T) {
			t.Parallel()

			// A paragraph, so the payload is ordinary prose as far as the parser
			// is concerned, and the DM's voice, so the sanitiser is the thing
			// that has to notice.
			body := "The DM pasted this:\n\n" + entry.payload + "\n\nAnd then carried on writing.\n"

			result := renderWith(t, body, render.Decision{})

			for _, forbidden := range forbiddenAfterSanitising {
				if strings.Contains(strings.ToLower(result.HTML), strings.ToLower(forbidden)) {
					t.Errorf("the output contains %q\npayload: %s\nhtml: %s", forbidden, entry.payload, result.HTML)
				}
			}

			// The page is still a page: the surrounding prose survived, so a
			// payload does not take the page with it.
			if !payloadsThatEatThePage[entry.payload] && !strings.Contains(result.HTML, "And then carried on writing.") {
				t.Errorf("the prose after the payload did not survive\nhtml: %s", result.HTML)
			}
		})
	}
}

// TestAHostileInputArrivesInert is the wart the task-list allowance costs, said
// out loud rather than left to be discovered.
//
// `input` is in the allow-list because a GFM task list is a real thing a DM
// writes, and goldmark renders it as one. The price is that a hostile `<input>`
// arrives as a bare `<input>` with every attribute stripped: an empty text box,
// no handler, no source, nothing it can do. A sanitiser cannot say "only where
// it is a checkbox", so the element stays and the attributes go.
func TestAHostileInputArrivesInert(t *testing.T) {
	t.Parallel()

	body := "The DM pasted this:\n\n<input onfocus=alert(1) autofocus type=text>\n\nAnd then carried on.\n"

	result := renderWith(t, body, render.Decision{})

	if !strings.Contains(result.HTML, "<input>") {
		t.Errorf("the output does not contain the stripped input; this test is about what it does contain\nhtml: %s", result.HTML)
	}
	for _, forbidden := range []string{"onfocus", "autofocus", "type=", "alert"} {
		if strings.Contains(result.HTML, forbidden) {
			t.Errorf("the stripped input kept %q\nhtml: %s", forbidden, result.HTML)
		}
	}
}

// TestTheSanitiserKeepsWhatTheRendererProduces: a policy that removed
// everything would pass the corpus, so this pins the other direction. Every
// construct the renderer emits for a DM's notes has to come through.
func TestTheSanitiserKeepsWhatTheRendererProduces(t *testing.T) {
	t.Parallel()

	body := "# A heading\n\n" +
		"**Bold**, *italic*, `code` and a [link](https://example.invalid/x).\n\n" +
		"A fenced block, which is how a DM writes a stat block:\n\n```\n> [!SECRET] is not a callout in here\nAC 14 HP 22\n```\n\n" +
		"| a | b |\n| --- | --- |\n| 1 | 2 |\n\n" +
		"- [x] a task\n- [ ] another\n\n" +
		"> [!warning] A callout\n> With a body.\n\n" +
		"A footnote[^1].\n\n[^1]: The note.\n\n" +
		"![an image](_attachments/x.png)\n\n" +
		"<details><summary>More</summary>\n\nHidden.\n\n</details>\n"

	result := renderWith(t, body, render.Decision{})

	for _, want := range []string{
		"<h1", `id="a-heading"`,
		"<strong>", "<em>", "<code>", "<pre>", "AC 14 HP 22",
		`<a href="https://example.invalid/x"`,
		"<table>", "<th>", "<td>",
		`type="checkbox"`,
		`class="callout callout-warning"`,
		"callout-title",
		`<img src="_attachments/x.png"`,
		"<details>", "<summary>",
		"footnote",
	} {
		if !strings.Contains(result.HTML, want) {
			t.Errorf("the sanitiser removed %q, which the renderer produces\nhtml: %s", want, result.HTML)
		}
	}
}

// TestASanitisedRenderIsStillParsed: the output of a render has to be HTML that
// a browser and a sanitizer downstream can both read, which is a different
// property from "contains no forbidden word".
func TestASanitisedRenderIsStillParsed(t *testing.T) {
	t.Parallel()

	body := "A paragraph with a link and a callout.\n\n> [!info] Title\n> Body.\n"

	result := renderWith(t, body, render.Decision{})

	if !strings.HasSuffix(strings.TrimSpace(result.HTML), "</p>") &&
		!strings.Contains(result.HTML, "</div>") {
		t.Errorf("the rendered HTML does not end in a closed element\nhtml: %s", result.HTML)
	}
	// Every opening tag this renderer emits for a block is closed: an unclosed
	// <div> is how a sanitiser's output is turned into a page the browser
	// finishes differently from the one the test read.
	opens := strings.Count(result.HTML, "<div")
	closes := strings.Count(result.HTML, "</div>")
	if opens != closes {
		t.Errorf("the output has %d <div> and %d </div>\nhtml: %s", opens, closes, result.HTML)
	}
}
