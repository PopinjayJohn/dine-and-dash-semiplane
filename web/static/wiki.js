// The reading layer: the search dropdown, the live page, and the toasts.
//
// It is a plain script with no framework, and the reason is the same as the
// editor's: it has to work on a machine with no network, it is reviewed code in
// this repository rather than a string in a handler, and a DM's wiki is a few
// hundred pages rather than a data grid. The vendored `datastar.js` is loaded
// alongside it and is the reason none of this has to deal with `fetch`.
//
// Everything here is a convenience. Every surface it drives has an ordinary URL
// behind it, so a blocked script costs a dropdown and a live page and nothing
// else — and `TestTheEditorWorksWithNoScript` is the test that says so for the
// editor, which is the one where losing it would mean losing work.

(function () {
  "use strict";

  // # The search box
  //
  // Datastar owns the request; this file owns when to ask. The debounce is the
  // whole of the difference between a search box and a denial of service, and it
  // is 120ms because that is Datastar's own default for `data-` bindings and
  // because a DM typing prose is not a machine.

  function wireSearch() {
    var input = document.querySelector("[data-search-input]");
    if (!input) {
      return;
    }
    var dropdown = document.getElementById("search-results");
    if (!dropdown) {
      return;
    }

    // The results URL is a template with `__DATA` in it, which is Datastar's
    // syntax for "put the bound value here". Reading it rather than building it
    // means the template is written in the markup, where the server put it, and
    // not in this file, where nobody looks.
    var template = input.getAttribute("data-search-results");
    if (!template) {
      return;
    }

    var request = buildRequest(template, input);

    input.addEventListener("input", function () {
      // An empty box is not a search, and asking anyway is a request per
      // keystroke for a dropdown that can only be empty.
      if (!input.value.trim()) {
        hide(dropdown, input);
        return;
      }
      request.req.value = input.value;
      // `apply=false` while typing: the box is not the dropdown, and a partial
      // re-render of the page under a reader's cursor is worse than no dropdown.
      dsEl("input", request, { apply: false });
    });

    // Escape closes the dropdown and returns the focus to the page, which is
    // what a person pressing it expects and what a keyboard user needs.
    input.addEventListener("keydown", function (event) {
      if (event.key === "Escape") {
        hide(dropdown, input);
        input.blur();
      }
    });

    // Clicking away closes it. Without this the dropdown sits over the page
    // forever after the first search, which is the second-most-annoying thing a
    // dropdown can do.
    document.addEventListener("click", function (event) {
      if (!dropdown.contains(event.target) && event.target !== input) {
        hide(dropdown, input);
      }
    });
  }

  // show and hide both keep `aria-expanded` in step, because a screen reader
  // announces that attribute and a dropdown that appears silently is a dropdown
  // nobody with a screen reader knows has arrived.
  function show(dropdown, input) {
    dropdown.removeAttribute("hidden");
    input.setAttribute("aria-expanded", "true");
  }

  function hide(dropdown, input) {
    dropdown.setAttribute("hidden", "");
    input.setAttribute("aria-expanded", "false");
  }

  // # The live page
  //
  // The stream is opened by a `data-stream` attribute on the page's own link, and
  // this file only decides whether to open it. The server does the rest: it
  // re-reads the page and re-renders it under the reader's own decision, so what
  // arrives is the page this reader is allowed to see — which is why there is
  // nothing here about secrets, and why there could not be.

  function wireLivePage() {
    var link = document.querySelector("[data-stream]");
    if (!link) {
      return;
    }
    var page = document.getElementById("page");
    if (!page) {
      return;
    }

    // The stream is the page's own URL with `?stream=1`, and a browser that is
    // about to be closed by a laptop lid does not need to be told.
    if (!window.EventSource) {
      // Safari before 7 and anything else without it. The page still works; it
      // just does not update by itself, and the "Live" link is there for a
      // person who wants the raw stream.
      return;
    }

    var stream = new EventSource(link.getAttribute("href"));
    stream.addEventListener("datastar-patch-elements", function (event) {
      applyPatch(page, event);
    });
    stream.addEventListener("error", function () {
      // EventSource reconnects on its own, and its retries are backoff, so this
      // is a log rather than anything a reader needs to see. A wiki that is not
      // live is still a wiki.
      console.debug("the page stream is reconnecting");
    });
  }

  // applyPatch replaces an element from a `datastar-patch-elements` frame, and it
  // is four lines because the frame is small: a selector and a set of elements.
  //
  // **The selector is honoured and the payload is not re-parsed as instructions.**
  // The only selector a stream of ours ever sends is `#page`, and a payload that
  // named anything else is dropped rather than obeyed. A stream is a channel the
  // server opens over the reader's own cookie, so a hijacked one could otherwise
  // rewrite anything on the page; making the one element it may touch a constant
  // means a hijacked stream can at worst put a stale page on screen.
  function applyPatch(page, event) {
    var lines = String(event.data || "").split("\n");
    var selector = null;
    var elements = [];

    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];
      if (line.indexOf("selector ") === 0) {
        selector = line.slice("selector ".length);
      } else if (line.indexOf("elements ") === 0) {
        elements.push(line.slice("elements ".length));
      }
    }

    if (selector !== "#" + page.id || elements.length === 0) {
      return;
    }

    var parsed = new DOMParser().parseFromString(elements.join("\n"), "text/html");
    var replacement = parsed.body.firstElementChild;
    if (!replacement || replacement.id !== page.id) {
      return;
    }

    // The heading and the timestamp in the replacement are what make an update
    // legible: a reader who sees the same words scroll past has been told nothing,
    // and the page carries a `time` element precisely so a person can see that it
    // moved.
    page.replaceWith(replacement);
  }

  // # Toasts
  //
  // A region the server can write into, for the three things worth interrupting
  // somebody about: a save that is about to be refused, a page that has just
  // changed under them, and a stream that has come back.
  //
  // `aria-live="polite"` and *not* `assertive`: a toast that interrupts is a
  // toast that steals a screen reader's place in the sentence they were reading.
  // Nothing this application has to say is that urgent.

  function wireToasts() {
    var region = document.getElementById("toasts");
    if (!region) {
      return;
    }

    document.addEventListener("datastar-toast", function (event) {
      toast(region, { message: event.detail && event.detail.message });
    });

    // And the two events this file can raise on its own, which are the two a
    // reader would otherwise notice by absence.
    document.addEventListener("wiki:conflict", function () {
      toast(region, { message: "Somebody else saved this page. Save again to see the difference." });
    });
    document.addEventListener("wiki:saved", function () {
      toast(region, { message: "Saved." });
    });
  }

  function toast(region, options) {
    var node = document.createElement("p");
    node.className = "toast";
    // `textContent` and never `innerHTML`: a toast's message can come from a
    // server response, and this is the one place in the front end where a string
    // from the network would otherwise land in markup.
    node.textContent = (options && options.message) || "";
    region.appendChild(node);

    window.setTimeout(function () {
      node.remove();
    }, 4000);
  }

  document.addEventListener("DOMContentLoaded", function () {
    wireSearch();
    wireLivePage();
    wireToasts();
  });
})();
