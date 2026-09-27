// The editor's script: a preview as you type, and an autosave.
//
// It is a plain script with no framework, and the reason is the same one the rest
// of this application is built that way: it has to work on a machine with no
// network, it is reviewed code in this repository rather than a string in a
// handler, and a DM's wiki is a few hundred pages rather than a data grid.
//
// **Everything here is a convenience.** The form posts to itself with a real
// `action` and the textarea carries the ETag in a hidden field, so with this
// script blocked — by a strict CSP, a browser extension, a network that dropped
// the file — the editor is a textarea and a Save button and still saves correctly.
// `TestTheEditorWorksWithNoScript` is the test that says so, and it is in the Go
// suite precisely because a JavaScript-only save path is not testable from here.
//
// The CSP allows this file because the layout puts a per-response nonce on the tag,
// and a nonced script element is authorised whatever its `src` is. There is no
// `unsafe-inline` and no `unsafe-eval` anywhere in the policy, so this file is the
// only script the editor runs.

(function () {
  "use strict";

  var form = document.getElementById("editor-form");
  if (!form) {
    return;
  }

  var textarea = document.getElementById("markdown");
  var pathField = document.getElementById("path");
  var preview = document.getElementById("preview");
  var saveState = document.getElementById("save-state");
  var previewButton = document.querySelector("[data-preview-button]");
  if (!textarea || !preview) {
    return;
  }

  // `autosave` is 1200ms, and the reason for the number is a DM typing prose
  // rather than filling a field: a preview that updates on every keystroke is
  // unreadable, and one that waits three seconds feels broken. It is longer than
  // Datastar's 100ms default because this is a *render of a whole page* on every
  // update, not a patch.
  var PREVIEW_DELAY = 400;
  var AUTOSAVE_AFTER = 30000;
  var previewTimer = null;
  var saveTimer = null;
  var lastSaved = null;

  // A CSRF token is a credential, so a preview must not put one in a URL: the
  // request is a POST with the token in the body, and the token is read from the
  // form rather than from anywhere else.
  function csrf() {
    var field = form.querySelector('input[name="csrf"]');
    return field ? field.value : "";
  }

  function setState(text, className) {
    if (!saveState) {
      return;
    }
    saveState.textContent = text;
    saveState.className = "page-meta" + (className ? " " + className : "");
  }

  // request posts the form's fields and hands back the response, with no
  // redirect-following: a save answers 303 to the page, and following it here
  // would throw away the editor and the unsaved state along with it.
  function request(action, onDone) {
    var body = new URLSearchParams();
    body.set("markdown", textarea.value);
    if (pathField) {
      body.set("path", pathField.value);
    }
    body.set("csrf", csrf());
    var etag = form.querySelector('input[name="etag"]');
    if (etag) {
      body.set("etag", etag.value);
    }

    return fetch(action, {
      method: "POST",
      body: body,
      credentials: "same-origin",
      redirect: "manual",
      headers: { "X-CSRF-Token": csrf() },
    }).then(function (response) {
      onDone(response);
      return response;
    });
  }

  // A frame is only swapped in whole. A short read means the render failed
  // half way through, and a pane holding half a page is worse than a pane holding
  // the last good one.
  function swapPreview(html) {
    var parsed = new DOMParser().parseFromString(html, "text/html");
    var body = parsed.body;
    if (!body) {
      return;
    }
    var only = body.children.length === 1 ? body.firstElementChild : null;
    if (!only) {
      return;
    }
    preview.replaceChildren(only);
  }

  function runPreview() {
    var action = form.getAttribute("data-preview");
    if (!action) {
      return;
    }
    request(action, function (response) {
      if (response.status !== 200) {
        setState("The preview could not be made", "is-error");
        return;
      }
      return response.text().then(function (html) {
        swapPreview(html);
      });
    }).catch(function () {
      // A network failure is the one case where the editor has to say something:
      // a DM who cannot preview and does not know why will assume the preview is
      // their fault.
      setState("The preview needs the server, which is not answering", "is-error");
    });
  }

  function schedulePreview() {
    if (previewTimer) {
      window.clearTimeout(previewTimer);
    }
    previewTimer = window.setTimeout(runPreview, PREVIEW_DELAY);
  }

  // autosave is a *scheduled* save, not a debounced one: a DM who types for a
  // minute without stopping gets one save, and a DM who types continuously gets
  // none until they stop. Debouncing an autosave into never is a common way to
  // ship a feature that appears not to work.
  function scheduleAutosave() {
    if (!form.getAttribute("data-autosave")) {
      return;
    }
    if (saveTimer) {
      window.clearTimeout(saveTimer);
    }
    saveTimer = window.setTimeout(function () {
      if (lastSaved === textarea.value) {
        return;
      }
      setState("Saving…");
      request("", function (response) {
        if (response.status === 409) {
          // A conflict is not an error to retry past: the page moved, and the next
          // thing to do is a human's decision. The script stops autosaving and
          // leaves the text alone.
          setState("Somebody else saved this page. Save again to see the difference.", "is-warn");
          form.removeAttribute("data-autosave");
          return;
        }
        if (response.status !== 303) {
          setState("Not saved. Press Save to see why.", "is-error");
          return;
        }
        lastSaved = textarea.value;
        setState("Saved.");
      }).catch(function () {
        setState("Not saved: the server is not answering.", "is-error");
      });
    }, AUTOSAVE_AFTER);
  }

  textarea.addEventListener("input", function () {
    schedulePreview();
    scheduleAutosave();
  });
  if (pathField) {
    // The path decides what the links in the preview resolve to, so changing it
    // is as much an edit as changing the prose.
    pathField.addEventListener("input", schedulePreview);
  }
  if (previewButton) {
    previewButton.addEventListener("click", function () {
      if (previewTimer) {
        window.clearTimeout(previewTimer);
      }
      runPreview();
    });
  }

  lastSaved = textarea.value;
})();
