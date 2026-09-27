## [Unreleased]

### Fixed

- **The CI smoke test asserted a string the page cannot contain**, and the reason
  is worth more than the fix. It grepped the campaign root for a page's
  `title:`, and the root lists *path segments* — and the campaign's own name is
  its slug. It also fetched the root with no session and expected to find a page in
  it, and a request that has not redeemed a link identifies nobody, so the read
  predicate admits nothing for nobody. Both assertions could never have passed, and
  `set -e` turned a working wiki into a red build. The step now asserts what is
  reachable without a link — a 200, the campaign, the "nothing here yet" notice —
  and adds the one assertion that can actually fail: a page with no session is a
  404.

### Added

- **The editor.** A textarea, a preview pane, a save, and the three ways a save can
  be refused — each with its own answer, because they are three different facts:
  not writable is a 403 (a player who just read a page is not told it does not
  exist), a conflict is a 409 with three texts, and a page that has gone is a 404.
- **The form posts to itself with a real action, the ETag and the CSRF token are
  hidden fields, and the save button is a submit.** A JavaScript-only save path is
  untestable from Go and unshippable on a machine where the script did not load, so
  `TestTheEditorWorksWithNoScript` posts the form the way a browser with scripting
  disabled would. Autosave is a convenience and the form is the feature.
- **`?edit=1` and `?new=1`,** and not paths, for the reason `?raw=1` and `?stream=1`
  are queries: a path segment would be a first-segment name the vault could not
  also use, so a DM with a page called `edit` gets it.
- **A preview is the same derivation, the same decision and the same component as
  the save**, so a preview cannot disagree with the save about who may see what —
  which is §9's "one render path" applied to a path nobody thought about when it
  was written. It writes nothing: a route that answered a question by doing the act
  would be a route a browser's prefetch could write to.
- **A conflict is three texts side by side and no merge.** A merge is a decision
  about somebody's prose, and a server that makes it silently is a server that has
  edited a DM's page without asking. The base column is empty — not guessed — when
  this application has not kept the text the edit was made from.
- **A preview of something that is not a page yet says why instead of failing**, and
  it is a 200: a DM typing a frontmatter fence is in that state for a second, and a
  pane that flashed a 500 while they typed would be a pane they stopped looking at.
- **`failWith` is a 500 that carries the reason, for exactly one case: the DM typed
  something that is not a page.** A DM whose `visibility: secret` is a typo and
  gets "500 Internal Server Error" learns nothing and files a bug; a DM who gets
  "visibility must be one of players, dm-only, dm-and-owner" fixes it. The person
  reading it is the person who can fix it, which is what makes it different from
  the usual rule that an error string must not reach a response.
- **`web/static/editor.js`, vendored and reviewed,** like the client: a preview as
  you type and a *scheduled* autosave rather than a debounced one, because a
  debounced autosave into never is a common way to ship a feature that appears not
  to work. The CSP allows it through the layout's per-response nonce, with no
  `unsafe-inline` anywhere.
- **The Edit link comes from the decision and is drawn only when a page may be
  edited.** A link the gate would refuse is a promise the wiki does not keep, and a
  DM who follows one and gets a 403 concludes the wiki is broken.
- **`wiki serve` wires the writers it already has open** rather than opening a
  second vault per campaign, and opens one on demand for a campaign added since
  the server started.

- **`?users=1`: the campaign's principals, and the button that mints a link.**
  §10 says the DM clicks "new player link" and the plaintext is shown *once*,
  and nothing in M0 through M8 had a button to click — the missing half of what a
  DM needs to hand somebody a link.
- **The minted link is shown once and only once.** The response is a 303 to the
  page with the link on it, and the page says it will not be shown again. The
  store keeps only a hash either way, so a second chance does not exist to give.
- **The list shows no token, no hash and no hint.** Four characters of a 32-byte
  credential is a fingerprint worth having in a list of six people at a table; the
  label the DM typed is what identifies a row to the person reading it.
- **The users page and the minting are DM-only, and a player gets a 403** — the
  URL is one a DM hands out, and a player who has found it knows the campaign has
  players, which is not worth a not-found and a 403 says what is wrong.
- **A DM cannot revoke their own link**, which is one click away from locking every
  player out of a campaign with no way back in but the data directory.
- **A revocation of somebody in another campaign is a 404**, because a revocation
  is a write on a row and a row in another campaign is not this DM's to end.
- **The four page tools are on the editor page** — rename, archive, purge, restore
  — because a DM who wants to rename a page is standing in its editor. They are
  POSTs to the editor's own URL with an `op` *field*, and the field is checked
  before the save.
- **A purge asks for a typed confirmation** and the page says what it loses, in
  three words: "There is no bringing it back". A browser's `confirm()` dialog is
  suppressed by a prefetch, is not announced by a screen reader, and a DM who has
  pressed Enter twice has a page they cannot get back.
- **The history is listed with the numbers the restore form posts back**, and a
  restore is a save, so it cannot overwrite a page that changed since the history
  panel was drawn.

- **[ADR 0019](docs/adr/0019-the-writer-checks-before-it-writes.md):** the
  writer checks before it writes, and the hub carries nothing. The order of a save
  is the design, and ADR 0017's residual — the gate checks ownership but not
  position — is closed by the gate being handed a *derived* page whose owner came
  from the path and the frontmatter, so there is nothing for a caller to assert.
- **[ADR 0020](docs/adr/0020-link-resolution-is-campaign-wide.md):** link
  resolution is campaign-wide, and that is a finding rather than a decision. A
  player can tell which paths exist from whether a link resolved; it is not a
  content disclosure, and the fix is decided and *not built* because M10 makes
  links more visible and the two changes belong in the same conversation. The fix
  is `domain.WithPrincipal` in the context, read by the resolver, with no change
  to the render cache key because `CanSeeSecrets` already discriminates the output
  completely.

### Fixed

- **A page tool was silently a save.** `?edit=1&op=purge` arrived with no
  `markdown` field and no ETag, so the save reported a conflict with itself and a
  DM's purge button appeared to be a save that cannot be saved. The op is read from
  the form and checked before the mode, because a tool is a different verb on the
  same URL and the more specific one has to win.
- **A templ component's early `return` is not an early exit**, so the purge tool
  drew a button on a player's editor after being told they are not a DM.
- **The preview rendered the frontmatter as prose.** The handler had the whole
  file's bytes and the renderer wants the body, so a preview put an `<hr>` where
  the `---` fences were and a heading out of the `title:` line. The preview is now
  one function in `internal/edit` that parses, derives, decides and renders — the
  same four steps the save takes, in the same code, because two places that each
  build a page are two places that can disagree about one.

### Added

- **`internal/edit`: the writer.** Every page save goes through it, and so does
  anything that later needs to write a page programmatically. Its save is seven
  steps in a fixed order, and the order is the design:
  1. check the path is a path a page can have;
  2. read the file that is there now;
  3. compare its hash with the one the caller last saw, and refuse on a mismatch;
  4. ask the store's write gate about the content being saved, before any of it is
     a file;
  5. write the file, atomically;
  6. re-derive the row through `internal/index`, as the caller;
  7. record the previous text as a revision, in the database and in `_history`.

  Step 6 is the same derivation a sync does, by the same function, so a row written
  by an editor and a row written by a watcher cannot be two different derivations
  of one file. Step 4 comes before step 5 because the watcher writes rows as the
  DM: a player's file on disk is a file the watcher will index, as the DM, into a
  page the gate refused.
- **A save takes the whole file, not fields** — frontmatter and body, as the DM
  would have it on disk. A save that took fields would have to decide what to do
  with the keys it does not understand, and the only answer that does not lose a
  DM's own YAML is to take their bytes.
- **`Versions` returns the three texts a three-way diff needs** — base, current and
  incoming — fetched on demand rather than carried on the error, so a save that is
  not in conflict does not pay for a diff nobody looks at. The base is found by the
  caller's own ETag, which is a fact only a revision holds, and it is *empty* when
  this application has not kept that text rather than a guess: a page the DM wrote
  in Obsidian has no revision here, and a diff that invented a base would be lying
  about where the edit started.
- **`Restore` is a save**, so it checks the ETag, it keeps the text it replaced as
  a revision, and it goes through the gate. A restore that is itself undoable and a
  restore that cannot overwrite a page that changed since the history panel was
  drawn both come from that one decision.
- **`Rename` moves a page and follows every link that pointed at it**, DM-only, and
  it writes the new file *before* the old one goes so a failure in the middle
  leaves two pages rather than none. §5 says a rename rewrites inbound links
  atomically, and without that every `[[link]]` in the campaign becomes unresolved
  the moment somebody renames a page.
- **The link rewriter touches the target and nothing else.** `[[from|alias]]`
  keeps its alias, `[[from#heading]]` keeps its fragment, `[text](from)` keeps its
  text, and every other byte of the page is the DM's. It is a scanner over the
  bytes with goldmark used for the one thing it is reliable about — *where* the
  code blocks are — because **a `WikiLink` carries no source segment** (the
  renderer's own parser says so), so an AST rewrite would have to re-render the
  page, and re-rendering a DM's markdown is the one thing this project must never
  do to a file.
- **Archive removes the file and keeps the row**, so it is recoverable; **purge
  throws the row, the revisions and the inbound links away**, and it is the second
  of the two rather than a stronger first.
- **`store.GetPageArchived` is the only way to reach an archived row**, and it keeps
  the read predicate — the only clause it drops is `is_deleted = 0` — so an archived
  page a principal may not read is still not found. The name says what it is
  because a flag on `GetPage` would be a way for a handler to turn a read into an
  administrative lookup by accident.

### Fixed

- **A character page created through a single-page sync was written unowned.** The
  owner resolution answers "a character page is its own owner" with the page's own
  id, and on the pass that *creates* it the row does not exist yet. A full sync
  fixed it on the second pass, which is why M4 never saw it; a single-path sync did
  one pass and stopped. `SyncPathAs` now settles the same way a full one does, and
  the loop is the same loop rather than a second implementation of "until nothing
  changes". A character page indexed once is a page no player is bound to and no
  player can read.
- **`archive` tested the wrong sentinel for "nothing indexed and no file"** — it
  asked `errors.Is(err, vault.ErrNotFound)` for an error the *store* returns, so
  the tolerance never applied and a sync of a path with no file reported a failure
  where there was nothing to do. It is exactly what an archive does.
- **A save that changes nothing does not grow the history.** Re-saving a page
  without changing it is what an editor does when somebody opens it and types a
  space and takes it back, and a history of identical copies is a history nobody
  can read and nobody can restore from.

### Fixed

- **The sync can write as somebody, and the store's write gate runs for an
  editor's save.** `SyncPathAs` threads a principal all the way to the row write.
  Until M9 every writer was the sync, which writes as the DM because indexing the
  DM's own vault is what a sync *is* — so threading it is the only way the gate
  (ADR 0017) can be in the editor's path at all. A handler cannot forget a
  function signature.
- **The gate is asked *before* the file is written, and that order is a
  correctness property rather than a convenience.** The index watcher writes rows
  as the DM, so a player's file sitting on disk for the length of a refused save
  is a file the watcher indexes, as the DM, into a page the gate would not have
  allowed. `Syncer.CheckWritableAs` derives the page and asks the gate without
  writing anything; the editor asks it, then writes, then re-derives.
- **A write is gated even when it would change nothing.** `SyncPathAs` writes only
  if the index is not already what the file says, so a player re-saving unchanged
  content never reached the gate — a no-op is neither a refusal nor a success, it
  is an absence. The pre-check deliberately does not consult settlement for the
  same reason, and `TestAWriteIsGatedEvenWhenItWouldChangeNothing` is the test
  that found it.
- **A conflicting owner is refused to a player and unowned for a DM.** A page under
  `characters/brian/` that declares `character: aria` carries the path's answer,
  not the frontmatter's, so the gate refuses it rather than admitting it on the
  strength of a key the page's own path contradicts.
- **`Syncer.OwnerPageID`, the character's page id for a path and a document**,
  exposed so M9's editor asks the same question through the same two rules as a
  sync instead of reimplementing `OwnerOf` and the `characters/<slug>` resolution.
  A second implementation is how a player's spell sheet ends up owned by nobody
  while the index says otherwise.
- **`store.CheckWritable`, the gate asked on its own,** for the same reason: the
  check has to come first and the file must not exist until it has passed.
- **`make generate` and `make generate-check`, and `generate-check` is in
  `make check` and in CI.** The templ output is committed, so a `.templ` edited
  without regenerating it is a template and a `_templ.go` that disagree, and the
  disagreement is invisible until the page renders the old thing. The check
  compares the bytes on disk before and after regenerating rather than asking git
  whether the tree is clean, because a contributor who has already run
  `make generate` and staged the result has a *correct* generated file.
- **The CI smoke test boots the wiki and asks it for `/_/healthz`**, which is what
  the M0 comment said M8 would replace. It then fetches a campaign root and asks
  the process to stop, because a server that does not shut down cleanly leaves a
  lock file behind and the next start waits out the stale window.
- **`wiki serve`: the HTTP server, the index watchers, and the lock that keeps two
  of either off one data directory.** The listener comes up *before* the index is
  read, so the port a DM is told about is a port that is already accepting
  connections — binding late is how a server prints an address and then refuses
  connections for two seconds.
- **The default address is `127.0.0.1:8080`, not `0.0.0.0`.** This is a wiki on a
  DM's own machine, and the Go default of every interface is the right default for
  a service and the wrong one for a thing holding a campaign's secrets behind a
  share link and nothing else. A DM who wants it on their LAN types
  `--addr 0.0.0.0:8080`, which is a decision they make rather than one they
  inherit.
- **`WriteTimeout` is deliberately not set.** A stream is a response that never
  ends, and a write timeout would cut every live page in the campaign at exactly
  the timeout. `ReadHeaderTimeout` and `IdleTimeout` are set, because a phone that
  went to sleep mid-request is a real thing at a table.
- **The shutdown is the reverse of the startup, and the hub is closed first.** That
  is the step that matters: closing it ends every open stream, so `Shutdown` has
  nothing long-running left to wait for. A `Ctrl-C` is a graceful shutdown and not
  a kill, because a kill leaves a lock file behind and the next start waits out the
  stale window before it can run.
- **A campaign's write lock is released on the way out, and `openCampaigns`
  returns the function that does it** so that a caller who drops it is a caller
  who has leaked a lock — the kind of leak that only shows up as a confusing error
  two minutes later.
- **Every campaign is synced once at startup**, so a vault that has never been
  synced is servable without a second command, and a campaign that cannot be
  opened is a warning rather than a refusal: a DM with two campaigns and one
  unreadable directory still gets to play the other one.
- **One watcher goroutine per campaign, and a watcher that fails logs and
  returns.** A wiki whose live updates stopped is still a wiki that can be read,
  and that is the right thing to be left with.
- **A sync that changed nothing sends nobody a fresh copy of themselves.** The
  watcher fires on every filesystem event, and Obsidian's save is several writes;
  pushing a frame per event would be a render per reader per keystroke.

- **A live page.** `?stream=1` on a page URL is a stream of that page's changes,
  and each frame patches the `#page` article and nothing else — so a reader who is
  halfway down the page keeps their scroll position and their place in the sidebar,
  which is the difference between a live page and a page that reloads itself under
  you.
- **The hub carries a *notice*, not content, and that is the security property.**
  The obvious design — the watcher renders the changed page once and hands the same
  component to everyone watching it — has no correct version: a DM and a player are
  watching the same page, the watcher can only pick one decision, and so either the
  DM's page is full of secrets in a player's stream or the player's is missing them.
  So each subscriber re-reads and re-renders under its own principal and its own
  decision, through the same `renderPage` the page route uses. One rendering path,
  one decision per reader, and the bytes on a player's stream are produced by code
  holding that player's decision.
- **A stream for a page its reader may not see is a 404, before the upgrade.** A
  stream that never sends anything is a connection a client reconnects to for ever.
- **A stream is bounded, and a full hub is a 500 with a `Retry-After`,** because a
  reader who is told "come back in a moment" gets a page that updates in a moment.
- **The keep-alive is a comment frame written by hand in the handler,** because
  `internal/sse` has four functions and none of them is a comment, and inventing a
  fifth is a change to ADR 0006 rather than a detail. The handler is the one place
  in the application that knows the framing's spelling, and the comment says so.
- **`PageChanged` is a function taking the hub, not a method on the application,**
  because the hub is the caller's — the same rule that says the caller closes the
  store it opened — and the only other thing it needs is the rule for a topic's
  name, which is a rule about how this package spells a page.
- **`Config.Hub` is required rather than optional,** for the same reason: a hub the
  application built for itself is one nobody can close.
- **`internal/http`: the web shell.** The chi router, the middleware, the
  handlers and the templates. A handler is handed everything the middleware
  decided and takes no principal as an argument, because a handler that takes a
  principal as an argument is a handler whose caller decides who the caller is.
- **The middleware order is the argument, and it is written down where the
  package is read:** request id → logging → recovery → security headers → session
  → campaign → redeem. Read from the inside out it says what each layer is for,
  and campaign is inside redeem because redemption has to be able to say "that link
  belongs to a different campaign".
- **`/_/healthz`,** JSON because the thing reading it is a script. It is under
  `/_/` because every campaign's URLs are under `/c/`, and it never needs a
  session: a health check that did would report a wiki as down every time a
  player's link was revoked.
- **The CSP is `default-src 'none'` with a per-response nonce** for the script and
  `'self'` for the stylesheet, plus `base-uri 'none'` and `frame-ancestors 'none'`.
  A nonce that repeats is a nonce that authorises a script an attacker injected
  into an earlier response, and `TestTheCSPNonceIsNotTheSameOnEveryRequest` is the
  test that says so.
- **`Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex` and `Cache-Control:
  no-store` on every campaign response,** per ADR 0003, and
  `X-Content-Type-Options: nosniff` on every response, which is what makes the
  asset handler's content-type table a correctness question rather than a nicety.
- **The query string is not logged.** `?k=<token>` is the share-link credential
  and `r.URL.String()` would put it in every log line, in every proxy in front of
  the server, and in whatever a DM pastes into a bug report. The log line is built
  from the method and the path and nothing else.
- **The CSRF token is an HMAC of the principal's id,** so a player cannot compute
  one, one principal's is not another's, and a token minted for one campaign is
  useless in another — the tenancy check happening in a form field. It is tied to
  the principal and not to the session, because a session rotation is a security
  event and a token that died with it would make a player reload at the worst
  moment.
- **A deployment with no CSRF secret generates one at startup** rather than
  falling back to something everybody knows.
- **`?raw=1` is a page's markdown,** under the same decision the HTML is made
  under, and the body that goes out is `render.PublicText` — the same function
  the public search index is built from, so a page found by a search and a page
  fetched raw contain the same characters by construction rather than by
  agreement.
- **`?raw=1` and `?stream=1` are query parameters and not path segments,** because
  a path segment would be a first-segment name the vault could not also use, and
  the vault is the source of truth.
- **The logout form is a POST and carries the token,** because a GET that ends a
  session is a session anybody can end, and a forged POST is refused with a 403.
- **`index.NewResolver` takes a `Lookups` interface instead of a
  `*store.Store`,** because `internal/http` needs a narrower view of the index
  than the sync engine does and a caller that has to assert its way back to the
  concrete type to build a link resolver is a caller whose interface is a lie.
- **The cookie name follows the deployment.** `__Host-wiki_session` in production,
  `wiki_session` over plain HTTP — because a browser refuses a `__Host-` cookie
  without `Secure`, *silently*, so a local deployment that kept the prefix would
  be a wiki where every redemption works and no page is ever readable. This is
  the one thing in the cookie attributes that is not ADR 0003's, and the reason is
  written on the constant.
- **`make cover` measures hand-written code.** Generated templ output is filtered
  out of the profile first, for the same reason `.golangci.yml` excludes it from
  linting: measuring it says something about the templates' element-by-element
  branches rather than about whether the application is tested. The filter is a
  filename and nothing else, and the raw profile is still written so a reviewer can
  diff the generated part separately. Hand-written coverage is 88.7%.

### Fixed

- **A share link is a reusable bearer credential, not a one-time code** — a
  finding, not a decision, and the first test to ask the question found that ADR
  0003's five steps do not rotate the token. The case it serves is a player who
  clears their cookies or wants the wiki on a second device, and the alternative
  is a DM issuing a fresh link every time a browser forgets somebody. The threat
  model's answer is revocation and expiry rather than rotation, and
  `TestALinkIsRedeemableAgainUntilItIsRevoked` asserts that revocation stops it.
- **The 500 page asks the store for nothing.** The store is the thing that has
  just failed, and an error page that lists a page tree is an error page that
  queries the database again — which is a second failure, and a panic inside the
  recovery handler is a panic that takes the process down with every other
  player's session on it. Found by a test that made the store *panic* rather than
  fail.
- **The uptime was a package variable reading `time.Now()`** while everything else
  used the injected clock, so a test with a fixed clock got minus five thousand
  hours. It passed as a duration string, which is what makes it the kind of wrong
  that survives review: it is a duration, it is a string, and it is nonsense.
- **The 404, the 500, the 403 and the 405 build their shell through one
  function,** because three hand-written ones had already disagreed about whether
  the CSRF token was in it and the 404 ended up with a logout form whose token was
  the empty string: a form that could never be submitted.
- **The read predicate now asks whose campaign the principal is.** It always
  asked which campaign the *caller* wanted, and never whether the caller belongs
  to it, so a `GetPage` for a page in Thornford made with a session for the
  Blackwater was answered by the role clause alone: a player got every
  `players` page in a campaign they have no link to, and a DM got every page in
  it. A share link is scoped to one campaign and so is a principal — the column
  is NOT NULL, which is why `store.AsDM` takes a campaign — and nothing between
  the cookie and the predicate connected the two.
- **Nothing had ever asked, because every caller so far passed the right pair of
  arguments.** The sync engine is the only thing that called a page-returning
  method before M8, and it passes `AsDM(campaignID)` and is therefore always of
  the campaign it is reading. A predicate that is correct for callers who get
  their arguments right is a predicate one handler away from a disclosure, and
  the first caller with a real principal is the HTTP layer.
- **The two campaign conjuncts are the same column and are not a
  redundancy.** The first is the campaign the caller asked about, which every
  page query needs; the second is the tenancy test. They are written out
  separately so that the second stays visible: a conjunct left out of a
  predicate is a predicate that is correct until somebody reads it.
- **Tenancy is not authorisation, so it is not in `access.For`.** The rights
  matrix is about who may read a page, and "is this person a member of this
  campaign" is a question whose answer is always the same, not 36 cells.
  `TestStoreReadPredicateMatchesResolver` is unaffected and must stay unaffected:
  the principals it builds are rows in that campaign, so the conjunct is a
  constant `true` across all 36.
- **A dozen test fixtures were building principals that cannot exist.** Every one
  of them was a `domain.Principal` with a role and no campaign, which the schema
  refuses to store; the new conjunct is what made that visible rather than a
  matter of taste. `TestStoreContract`'s "a path in two campaigns is two pages"
  was the clearest of them: it built its DM from a *page* id.
- The four golden search statements gained the conjunct, and the placeholder
  count is written out as a sentence rather than computed, so the next conjunct
  has to be added to the sentence too.

### Added

- **`web/`: the static half of the front end, embedded.** The stylesheet and the
  pinned Datastar client, with `go:embed` and nothing fetched at runtime. ADR
  0006 requires the offline property and ADR 0011 explains why it matters — a
  table is a room with more than one device on it and no reason to have a working
  internet connection — and a CDN is also a third party in the path of a
  campaign's secrets, because a `datastar.js` fetched at runtime is a script the
  application did not write running with the session cookie.
- **`datastar.js` v1.0.4 vendored verbatim**, as ADR 0008 pins, and a test reads
  the version out of the embedded bytes so an upgrade that forgets the constant
  fails instead of passing quietly. The source map it names at the end is not
  vendored: it is a developer convenience, and a browser that cannot find it says
  so in a console and otherwise reads the file the application actually serves.
- **Assets are served by a handler rather than a raw `http.FileServer`**, for two
  reasons that are correctness and not polish. The content type is explicit,
  because the HTTP layer sends `X-Content-Type-Options: nosniff` and a `.js`
  served as `text/plain` is a script the browser refuses to run. And the ETag is
  computed from the bytes, because embedded files have a zero modification time,
  so `If-Modified-Since` never matches and a DM who replaces the binary would be
  left with last month's stylesheet in a browser cache that never asks again.
- **The stylesheet has no web font and no framework.** The system font stack is
  the right font for a tool a DM opens on their own laptop, and one file with no
  build step is one fewer thing to go wrong on a machine nobody has a shell open
  on.
- **The stylesheet matches the renderer's class names rather than inventing
  them** — `wiki-link`, `unresolved`, `embed`, `callout`, `callout-<type>`,
  `revealed`, `stripped` — and says so at the top, so renaming one is a change to
  two files and both say why.
- **There is no rule that hides a secret.** A `[!SECRET]` block a principal may
  not read has already left the parse tree before any HTML is written, and the
  only thing left is the marker the renderer emits instead.

- **`internal/sse`: ADR 0006's four functions, and the hub behind them.** A
  handler takes a `templ.Component` and never touches `text/event-stream`
  headers, `data:` prefixes or event ids. The implementation is the standard
  library one from the spike, and the spike stays a separate module run by
  `make spike`, so the record that datastar-go produces the same bytes cannot rot
  without CI noticing.
- **The type is `Sender`, not `Stream`.** ADR 0006 writes
  `func Stream(w, r) *Stream`, and Go has no room for a function and a type of
  the same name in one package, so one of them had to give. The constructor gave,
  because that is the name a handler writes.
- **`Hub` fans one change out to every stream watching it**, per topic rather
  than through one global channel: a handler for one page filtering out every
  other page's updates is a correctness question wearing a performance
  question's clothes.
- **The hub is bounded and refuses rather than evicts.** A stream is a goroutine
  and a socket, and a share link pasted somewhere reachable is a stream nobody is
  counting. An eviction silently closes somebody's stream; a refusal is an error
  the handler can turn into a `503`.
- **A slow reader loses a frame, and that is safe here and would not be
  elsewhere.** An update is a *whole element*, so the next one carries the page
  as it is then and a skipped frame is one the reader would have replaced
  anyway. A hub carrying deltas cannot drop, and the drops are counted rather
  than swallowed so a test — or a curious DM — can see them.
- **Closing the hub drains every stream** by closing its channels rather than
  setting a flag, so a handler ranging over one ends without checking anything.
  That is the shutdown property ADR 0006 asks for.
- **A resolved link carries the campaign it is in.** `/c/locations/rivergate`
  became `/c/blackwater/locations/rivergate`, and the campaign is now a field of
  `render.Page` rather than a thing the link path assumed. A data directory holds
  several campaigns (ADR 0011), so a campaign-relative path was never a URL: the
  link pointed at whichever campaign the reader happened to be in, which for a DM
  with two campaigns is the wrong campaign's town.
- **`render.PageURL` is exported, because four things link to a page.** A
  rendered wiki link, the page tree, a backlink and a search result are four
  callers of one rule, and the second implementation of "where does a page live"
  is the second thing to get wrong in production.
- **The campaign is in the render cache key.** Two campaigns can each hold
  `locations/rivergate` with byte-identical content — two DMs who both started
  from the same template — and a shared cache entry would have served one
  campaign's URLs inside the other's HTML. Same class of mistake as the decision
  not being in the key, and found by writing down the test that asks it.
- **A page rendered with no campaign resolves nothing.** A caller that has not
  said which campaign it is rendering for gets visibly unresolved links rather
  than links to a plausible wrong place: the same fail-closed answer this project
  gives everywhere else, and the thing that makes a handler forgetting the field
  a visible bug rather than a subtle one.
- `RendererVersion` is 2, because the output changed for the same input.

- **`internal/auth`, and the minting half of it.** 32 bytes from `crypto/rand`,
  hex-encoded, shown once and never stored. What is kept is the SHA-256 and four
  characters of it, so a dump of the campaign database identifies a token and
  cannot use one. The hash is SHA-256 and not a password hash on purpose: a token
  is 256 bits of entropy, so there are no cheap inputs for a KDF to defend
  against, and one would be 100ms of latency on every redemption to slow an attack
  that already cannot succeed.
- **`Token` is a type that cannot print itself.** A bare string is the shape of
  every other value in this program, and the one value that must never be logged.
  It implements `String`, `GoString` *and* `Format`, and the last two are not
  belt-and-braces: **`%#v` does not consult `String`**, it prints the Go-syntax
  representation, and **`%x` on a struct hex-encodes its fields** — so a Token
  with only `String` hands the whole credential to `t.Errorf("%#v", err)` and to
  the struct-dumping log handlers. Both were found by the test that checks every
  verb, and `Format` closes the set rather than closing the two that were noticed.
- A minted link is refused when there is nowhere to point it, no campaign to scope
  it to, a role that is not one, or **no label** — because the label is the only
  thing that tells two links apart in the DM's list, and a list of six links with
  no labels is a list of six sixteen-bit numbers.
- **`pages.owner_character_page_id`, and the sync resolving to it.** M4 worked out a
  page's owner on every sync, validated it, reported the problems and then threw
  the answer away, because there was nowhere to put it. It is a *page id* and not a
  slug, and that is the design: the read predicate asks a question about a page
  and answers it by joining, and a slug would put a second rule — how a slug
  becomes a page — inside the one place that must have exactly one.
- **The whole subtree has the character's own page as its owner, not itself**, and
  the read predicate now correlates on that owner rather than on the page. This is
  the difference between "Alice may read her character's backstory" and "Alice may
  read exactly the one file she happens to be bound to", and it is the spec's form
  of the ownership test; M6 had to correlate on the page itself because the column
  did not exist yet.
- **A principal on every page-returning store method** — the other half of
  invariant 3, and the one that has been outstanding since M4: `GetPage`,
  `GetPageByID`, `ListPages`, `FindPageByAlias`, `FindPageByName` and `Backlinks`
  all take one now. A signature that can be called without naming a principal is a
  method whose ACL is somebody's decision per call site, and a method that *can* be
  filtered and does not is worse than one that cannot.
- **A page that exists and may not be read is `not found`, not "forbidden."** The
  distinction tells a player which paths a DM has written, and a path is enough to
  ask about.
- **`Backlinks` filters on the *source*.** A backlink says "some page mentions this
  one", and if the some page is a `dm-only` session log then the backlink is a
  disclosure: it hands a player a path, and a path is a thing they can then try to
  read and be told 404 about.
- **`GetPageByID` and `Backlinks` scope by the principal's campaign**, because they
  have no campaign argument of their own — so a principal from another campaign
  gets not-found rather than somebody else's page. That is also why `store.AsDM()`
  **takes a campaign**: the first version did not, and every page in every campaign
  became invisible to the thing that indexes it. A principal without a campaign
  cannot exist — the column is NOT NULL — and a DM of no particular campaign is not
  a principal at all.
- The alias and name lookups filter, so a `[[link]]` into a page a player may not
  read resolves to nothing rather than to a page.

- **`internal/access` is the one place that answers "what may this principal do
  with this page"**, and it is pure: no store, no clock, no mocks. That is not
  minimalism, it is the property the rights matrix needs — 36 cells can only be
  written down and run in a millisecond if deciding one is a function call.
- **`TestStoreReadPredicateMatchesResolver`, deferred since M5.** The SQL
  predicate and the Go resolver answer the same question about the same matrix,
  and the two are two implementations for a reason: the predicate has to be SQL
  and the resolver has to be a function. So the answer to "which one is right" is
  that both are required to say the same thing, and this test is what makes it
  true. Thirty-six cells, §8's two principals plus the row the matrix does not
  have, compared through both the read scope and the stricter secret scope.
- **The M6 placeholder `render.Decision` is now an alias for `access.Decision`.**
  M3 promised this in a comment, and a comment is not a thing.

- **`body_public` is no longer empty, so a page is findable by its prose.** It is
  filled from `render.PublicText`: the page's own text with its unrevealed secrets
  removed, as **plain text rather than markdown**, because the column is read by
  the tokenizer and by nothing else — a markdown version would need the byte range
  of every secret callout, and the tree does not carry one, so it would mean a
  second parser for the same grammar. This is the case that has been "a missing
  feature, in the safe direction" since M5, and it became safe to fill because
  **the public search's rows are filtered by the read predicate**: every
  principal who can reach a hit in this text may read the page, so the index holds
  exactly what a reader of that page may read.
- **A revealed secret stays in the public half.** §9 says a `[!SECRET]{.revealed}`
  block is visible to everyone who can read the page, and every principal the
  predicate admits for that page may read it — so stripping it would be removing
  text the reader is entitled to. A *callout's title* is in neither half, because
  a title is an attribute on the node rather than text in it.
- A forbidden-substring test from a file on disk to a search result: the secret's
  word is findable by nobody through the public path, and findable by the DM
  through the private one — the second half being what makes the first mean
  something, since an index with no secret text in it would also pass it.
- **One parser, one lock, one path into it.** goldmark's `Parser` is shared by
  every render, every link resolution and every secret extraction, and whether
  that is safe is a fact about goldmark's internals that this project concluded by
  reading them: goldmark documents nothing, and the field I suspected of being
  per-parse state turns out to be written once inside a `sync.Once`. **So this is
  defensive and the commit says so** — I could not provoke a failure with the lock
  removed and eight goroutines at it, and claiming a data race I cannot show
  would be exactly the kind of thing this changelog exists to prevent. What *is* a
  real defect: the first version of the fix had two mutexes guarding one parser,
  because the package-level pipeline **is** a `*Renderer`. There is now one lock
  and one way in. The tests beside it pin the behaviour that is actually ours —
  a secret is never in the public text and a render is never wrong about one,
  however many goroutines are doing it.

- **`render.Decision` is now `access.Decision`**, aliased rather than replaced with
  a struct of our own. M3 shipped a placeholder with one field and a comment
  promising this; a promise in a comment is not a thing. A one-field type of our
  own would be a *second* answer to "may this principal see the secrets on this
  page", and the whole of the renderer's safety is that there is one place that
  answers it.
- A test that puts a **decision straight from the resolver into a render** and
  checks the bytes: a DM sees a `dm-only` page's secrets, a player sees none of
  them *even when they own the page*, an owner sees their character page's
  secrets, and nobody sees a `players` page's. Forbidden-substring over the raw
  HTML, never a DOM.
- **The cache key carries the one field of the decision that changes the bytes**
  (`CanSeeSecrets`), and there is now a test for that narrowing rather than a
  sentence in a comment: two decisions differing only in the read, edit and reveal
  fields must render identically, and a field added later that *does* change what
  is stripped is a field the key has to grow. A cache key missing that field is a
  render made for a DM served to a player, and it is silent.

- **The write side of the rights matrix: `UpsertPage` takes a principal.** A
  read-only predicate with an open write path is a building with a locked front
  door and an unlocked back one — a player who cannot read a page can still write
  it, and the next sync indexes what they wrote and it appears in somebody else's
  list. The check is in the store rather than in each handler, because the other
  shape makes "the check that must not be forgotten" a line of code in a file
  whose other job is turning a request into HTML.
- **A refusal is `ErrNotAllowed` and deliberately not `ErrNotFound`.** A read must
  not confirm that a page exists; a write is a request about a page the caller
  already holds, and telling a player their link is dead sends them to the DM with
  a different question. The asymmetry is the point.
- **It checks ownership, not position, and says so.** A page whose
  `owner_character_page_id` is empty is refused to a player, and one whose owner is
  a character they are not bound to is refused. It does *not* check that the path
  lies inside that character, because that is `internal/index`'s `OwnerOf` and
  duplicating it here would be a second implementation of the same rule. The
  residual is named in the file: a caller that supplied its own character as the
  owner of a page elsewhere would be caught by the path check and not by this one.
  No caller can do that today — the only writer is the sync, which reads as the DM,
  and the player-facing one arrives with the editor.
- **Reveal is a level change and only ever loosens one.** The owner may open their
  own page's audience; a player may not *tighten* one, because a method that could
  close a page is a method a player could use to make their own notes vanish from
  the DM's page tree. `SetPageVisibility` reads the page as the DM and gates on the
  write rule, so a principal who cannot read a page gets "you may not" rather than
  "there is nothing here".
- A gate that does not consult the resolver would be a third implementation of the
  matrix, so the store's ownership question is the same one the predicate asks, and a
  test checks that the gate and `access.For` agree on the same page.

- **`internal/access`, the one place that answers "what may this principal do with
  this page".** `For(p, page) Decision` is pure — no store, no clock — because the
  rights matrix has 36 cells and the only way to be sure all 36 behave as
  documented is to be able to write down all 36 and run them in a millisecond. Its
  two arguments are the two facts the matrix uses, which is why they are not
  `domain.Principal` and `domain.Page`: a resolver holding a campaign id has a
  temptation to check it, and that check is a rule the store already applies to
  every query.
- **`TestForDecisionMatrix` writes every cell out by hand** — 24 of them: §8's two
  principals, three audiences, two ownership states, two archived states.
  Generated would have been a matrix and a generator, and when they disagree the
  generator reads as authoritative, which is how a table of tests becomes a
  restatement of the code. It **caught a disclosure in the resolver two minutes
  after it was written**: the ownership shortcut was applied before the `dm-only`
  check, so a player bound to a character page the DM had marked `dm-only` could
  read it, edit it and see its secrets. §8 says `dm-only` is *absolute*, and the
  only reason that is written down rather than implied is that somebody eventually
  writes the matrix.
- **`TestStoreReadPredicateMatchesResolver`, the named test this project has been
  deferring since M5.** The SQL predicate and the Go resolver answer the same
  question about the same matrix, and the two disagreeing is a leak in one
  direction and a missing feature in the other, so the answer to "which one is
  right" is that both are required to say the same thing. Thirty-six cells, §8's two
  principals plus the row the matrix does not have, compared through both the read
  scope and the stricter secret scope. It is worth more than the code it checks,
  because it is the only place the matrix, the column default, `ON DELETE SET
  NULL`, the archived rule and SQL's own NULL handling are compared against the
  thing the documentation says.
- **A player can now find their own character's secrets.** The private index was
  readable by the DM and by nobody else, so before this milestone a player's own
  character page's secrets were findable by nobody at all. A binding is what makes
  ADR 0007's rule ("a character-owned page's secrets are readable by its owner")
  reachable, and it is why the binding table and the ownership clause shipped
  together.
- **A role or binding change ends every session of that principal, in that order.**
  The order is the point: the change is written first and the sessions ended second,
  so a failure between them leaves a principal with the *new* role and stale
  cookies, which the predicate resolves in the safe direction — a demoted DM's
  stale cookie is a player, not a DM.
- **Unbinding somebody takes effect on the next request.** A session is a row, and a
  row is not told that what it may read has changed, so a player who was just
  unbound would keep reading the page for as long as their cookie lived — a
  fortnight. This only works because sessions are rows rather than signed blobs,
  and it only happens because the change says so.
- **Saving a role that was already the role rotates nothing.** A DM who opens a
  settings page and presses save should not end every session in the campaign.
- Three new audit actions, `role_changed`, `binding_changed` and
  `session_rotated`. They are their own actions rather than details on a principal
  change because "when did Alice's browser stop being a DM's browser" is a question
  of its own, and answering it from a detail field is answering it from a string
  somebody typed. A binding's detail is a **count and not the page ids**: the log is
  what somebody pastes into a bug report.
- `Store.EndSessions`, which is not revocation — revocation is a fact about the
  link, and this is the housekeeping after a change that made every session stale.

- **A redacting logger, and `TestNoTokenInLogs`.** The named test runs a *full
  auth flow* — mint, log the URL and the token, redeem, authenticate, fail a
  redemption, log out — and then greps everything it produced for every token it
  touched. Both halves matter and are not the same check: a test that only
  exercised the redaction unit would pass against a logger that redacts and a flow
  that never logs the token, which is a logger nobody has connected to the thing
  that matters.
- **Redaction is by attribute name *and* by shape**, because a caller who logs a
  whole URL is doing something a shape check alone only catches by luck. A caller
  who logs something under the key `token` is telling us what it is.
- Two bugs the tests found in the redactor itself. **`Redacted` checked the whole
  remaining string for hex rather than the 64-character window**, so a token at the
  start of a sentence followed by a space was not recognised — a leak in every
  message anybody ever wrote. And **a token's SHA-256 is 64 hex characters, and so
  is the token**, so the shape check redacts a hash along with a credential; the
  plan had been that a hash is a fingerprint worth logging, the cost is one
  correlation, and the log carries the principal id anyway. Requiring a delimiter
  after the window would have left "redeem `<token>`1" unredacted, so a 65-character
  hex run has its first 64 redacted and the last one left — a decision, and the
  wrong version of it is easy to imagine.
- **The redaction is a handler, not a rule callers remember**, so a caller who logs
  a request struct is covered by the same rule as one who logs a token
  deliberately, and a JSON logger gets the same redaction rather than a way round
  it. Redaction that leaves nothing readable teaches a DM to stop reading the log,
  which is how the next real problem goes unnoticed — so the test also asserts
  that the log still says what it did.

- **A rate limit on redemption**, ten attempts a minute per address, and the
  named test `TestRateLimitedRedemption` is written so that a limiter refusing the
  *second* attempt cannot pass it. Ten is about right for a campaign at a table: a
  player redeems once, a DM redeems twice testing a link they just minted, and a
  legitimate player never reaches the limit — a limit a real player hits is a
  player locked out of their campaign with no cause.
- **The limit counts the requests it refuses.** A limiter that does not is a
  limiter that refuses the tenth and then allows the eleventh, which is a limit of
  one. And it runs on the *redemption*, not on the store lookup, so a player who
  fumbled nine times and then pasted the right link is a player.
- **A forwarded header is believed only from a proxy this deployment is
  configured to trust.** A forwarded header is attacker-controlled on any direct
  connection, so believing it unconditionally means the limit is bypassed by
  sending a fresh `X-Forwarded-For` per attempt — exactly the attack the limit
  exists to slow. A request with no identifiable address is counted *together*
  with every other such request, because a limiter that skips unidentified
  requests makes "hide your address" a way to be unlimited.
- **The limiter is bounded and the bound is testable.** Every attempt from a new
  address allocates, so a botnet gets a free allocation each; past the bound the
  oldest window is dropped and the limit becomes approximate, which is the
  documented price. `Tracked()` exists so the bound is an observable claim rather
  than a comment. The closed-window sweep is **amortised rather than run on every
  check**, which the 20,000-distinct-addresses test found the hard way: O(n) per
  check is quadratic under exactly the burst the bound exists to absorb.

- **Redemption, and the token leaving the URL.** A token is parsed, hashed, and
  looked up by its hash in a UNIQUE column; then the campaign, the revocation and
  the expiry are checked; then the session is created, the last use stamped and
  the redemption recorded — and only then is a redirect handed back, built from
  the campaign's slug rather than from the request. The redirect is the one
  response in the program whose entire job is to send a browser somewhere, so a
  target that came from the incoming URL would be a target an attacker chose.
- **A token either matches its hash or it is not the link.** Nothing compares a
  presented token to a stored value, so there is **no constant-time comparison
  here and none is needed** — the lookup is a b-tree search on an index, not a
  scan, and there is no comparison whose duration could depend on where the first
  differing character is. The property that *is* asserted is that a truncated,
  prefixed, doubled or one-character-different token is refused. The §10 hardening
  item and this reasoning are recorded in ADR 0016.
- **A wrong token and a wrong campaign are different errors**, and revocation and
  expiry are separate from both. A DM who pastes a link into the wrong campaign is
  told so, because that is a mistake they can fix. A player whose link was revoked
  is told *that*, because it is not a secret — it is the answer to why their
  browser stopped working, and "not valid" would send them to the DM to ask. A
  caller who could tell the three apart could use the difference to learn which
  campaigns a DM has links for, so the two token failures are one error.
- **A lapsed session is deleted, not merely refused.** A row that is found and
  rejected on every request for the rest of its life is a row nobody will ever
  prune, and revocation is a delete; expiry is the same idea for the clock's
  version of it.
- **A refused redemption records nothing.** A log that said "this link was used"
  for a token that was not would be worse than no log, because it sends a DM
  looking at the wrong player.
- `Config` holds the two windows in one place, because a link's expiry and a
  session's lifetime are *one* policy and not two — and two structs with a field
  each is a way to set them out of step.

- **A link has no expiry by default**, which is the default most DMs want: a link
  that quietly expired would arrive as "my player's link stopped working" with no
  cause, and revocation is the thing a DM thinks of doing.
- The store seam is **nine methods, declared by this package** — by the consumer,
  so a second implementation is a compile error away rather than a runtime
  surprise.

- **Share links, sessions, an audit log and character bindings**, and the store
  methods that keep them. `principals`, `sessions` and `audit_log` were already in
  the base migration, written down before any of them was needed; this milestone
  adds the one that could not be — `principal_characters`, a table whose rows come
  from a decision somebody makes later, about a page that may not exist yet. It is
  also what the read predicate has been waiting for: its ownership test ships as
  `1 = 0` because no principal owned anything, so every `dm-and-owner` page
  belonged to no one. An empty binding table and a missing one behave the same, so
  shipping it early changes no answer.
- **A revocation is the flag and the sessions, in one transaction.** A player whose
  link was pasted into a Discord channel has to be logged out *now*, and a revoke
  that set `revoked_at` and then failed to delete the sessions would leave a
  browser working with a link the DM believes is dead. Revocation has three
  answers and a DM clicking a button can tell them apart: not found, already
  revoked (a no-op, not an error — a DM who clicks twice must not be told
  something is wrong), and revoked now. The row is flagged rather than deleted,
  because the audit log's question is "was this link ever used" and a deleted
  principal cannot answer it.
- **"Everyone sign in again" is scoped to one campaign**, and a DM with two
  campaigns cannot log out the other one's players by accident. The sessions of
  *already*-revoked principals in that campaign are cleared too, which repairs the
  state a failed revoke leaves.
- **A stored session always has an expiry.** `domain.Session.Expired` treats a zero
  expiry as "never", so a zero-valued struct is safe to ask; the store refuses to
  *store* one, because a session with no expiry is a credential that outlives the
  reason it was issued. The boundary is `>=`, so a session whose expiry is exactly
  now is expired — the same rule as `Expired`, and the same direction, because
  being wrong by the smallest possible amount is still being wrong.
- **A principal is found by the SHA-256 of its token and never by the token**, which
  is a property of what the method takes rather than a promise about discipline. The
  hash is UNIQUE, so two principals sharing one token is a conflict: two accounts
  for one credential, and a DM with two links in their list and one that does
  nothing.
- **A character binding is a replace, not an add**, because a binding is a statement
  about what a player owns *now*. A player who is given a new character and loses
  the old one has to stop reading the old one's pages on their next request, and an
  add-only table is a table where that does not happen. A duplicate page id is one
  binding rather than an error, because the caller is usually a sync and a sync
  should not fail a campaign over a page bound twice.
- All of it is **in the store contract suite**, so a second `Store` is held to the
  same split: a link found by its hash and never by its token, a revocation that
  ends sessions, a stored session that expires, a binding that replaces, and an
  audit log read newest-first.
- `OwnerExists` answers **false for a blank principal** rather than an error. A
  request that failed to identify its caller has no principal, so nobody owns the
  page, so the `dm-and-owner` branch of the predicate is empty. An error there
  would turn "not logged in" into a 500 for the one caller who must not see the page.

- **The sync keeps both search indexes in step.** A page is indexed from its file
  on the same pass that writes its row, and the settled check asks whether the
  index already holds what the file derives — so a page whose search rows were
  deleted behind the sync's back is rewritten even though nothing in the vault
  moved and every hash still matches. That is the rot-in-place failure one layer
  up from the page row, and comparing the hashes would not have seen it.
- **An archived page is not findable**, and only its findability went: the row
  survives so the archive stays recoverable, but a search must not name a page no
  read can open, because the error a player gets for opening it says the page does
  not exist, which is a different answer from "you may not see it".
- A page is findable end to end from a file on disk: **title, aliases, tags and
  type today; prose once the redaction lands.** The body is *not* in the public
  index yet, and that is a missing feature rather than an oversight — working out
  which text a principal may be shown needs access control, and until that exists
  the only safe value for `body_public` is the empty string.
- **Secret text *is* indexed now**, because which text is secret is a *parsing*
  question and the parser already answers it. A DM can find their own secrets by
  content from this milestone on, which is the feature ADR 0009 exists for: the DM
  forgets which page they wrote a name on.
- A test that runs the whole chain — file, sync, query — because the chain is where
  a decision made in one package quietly fails to reach another, and no unit test
  in either package would notice.

- **Reciprocal Rank Fusion** merges the two ranked lists: a page's score is
  `Σ 1 / (60 + rank)` over the lists it appears in, so a page whose title matches
  *and* whose secret contains the phrase outranks a page whose title matches
  alone. BM25 scores from two indexes over two corpora of different sizes are not
  comparable, and any normalisation that made them agree would be a constant
  fitted to one corpus. `60` is the published default and is not tuned.
- Where a page matched in both, the **excerpt from the private index wins** — it
  is the more specific answer, and it is safe because the page is in the private
  list at all only if the stricter scope admitted this principal.
- Ranks are **renumbered** after the merge, because rank 2 of two lists is not
  rank 2 of one. Ties break on the best rank any list gave, then on the order the
  pages arrived in — not on the id, which a reader cannot see. Two identical
  requests give the same list.
- `search.Run` is the entry point: parse, refuse or stop early, one query per
  index, fuse, cut. It takes the backend as an argument and holds nothing, so the
  whole of it is testable with a fixture and no database — which is what makes
  the relevance and ordering tests as many as they are.
- Each index is read **five times deeper than the requested result count**,
  because RRF can lift a page that was eleventh in one list and first in the other
  and a fusion over two lists of exactly N cannot find it. A bound rather than a
  fit, and not tuned.
- A filter that names something the caller may not see is not information, so
  `is:dm-only` for a player is the empty intersection rather than an error, and a
  failure in either query is reported with the index named rather than dropping
  the private half and answering with a list that looks complete.
- **Benchmarks**, which is what ADR 0009 asks for where it says "kilobytes" and
  "roughly double". Over 400 synthetic pages of 200 words, on the machine this
  was written on: writing both indexes 229ms, the settled check 84ms, a one-word
  search 5.3ms, a search that matches nothing 0.4ms, and the fusion itself
  0.09ms with no database in the process. The numbers are a baseline to compare
  against, not a target.

- `render.SecretText` lifts a page's **unrevealed `[!SECRET]` text off the parse
  tree**, which is what the private search index is fed. It is in `render` rather
  than in the indexer because the `[!SECRET]` grammar has to be read the same way
  twice — the renderer strips a secret and the indexer has to decide which side of
  the split it belongs on — and two implementations of the same syntax is two
  answers waiting for a DM to write the callout that separates them.
- What goes in and what does not: a **revealed secret is public body, not secret
  text**, and the walk carries on *into* it because a revealed block can contain a
  secret that is not revealed. A blockquote with a secret's shape that the parser
  could not read is included, for the same fail-closed reason the stripper removes
  it — a secret that cannot be found is a secret the DM has lost. A secret inside
  a secret is counted once, because the outer one already carries the inner one's
  text.
- Two things the first version of it got wrong, both found by the tests: goldmark
  keeps a **code block's lines off the child nodes**, so a secret whose credential
  is in a code block indexed as an empty string; and a **soft-wrapped line** —
  which is every line a DM writes — arrives as two text nodes with the newline and
  the `> ` marker between them and nothing to say so, so a two-line secret was
  indexed as one run-on line whose phrases matched nothing. The boundary is now
  recovered from the source offsets.

- **Search runs against the public index**, filtered by the audience scope in
  SQL. It finds pages by title, alias, tag and page type, ranks a title match
  above a tag, an alias or a body match, and returns hits with a rank and an
  excerpt rather than a score — because a BM25 number from one index means
  nothing next to one from the other, and the fusion only uses the order.
- **The private index is reachable only through the stricter scope**, and a
  secret's excerpt is built from the private index. A DM can find their own
  secret by content, which is a real need: the DM forgets which page they wrote a
  name on. A player gets nothing, and the reason is not that the page is
  unreadable — the town page is perfectly readable — but that the secret inside it
  is not theirs to see.
- `TestSecretNeverAppearsInAResult` is the named test, and it is blunt: a
  forbidden-substring assertion over every field a hit carries, run for the
  canary, for fragments of the canary, and for the three ways a `dm-only` page
  could otherwise be listed. It also asserts the DM *does* find it, because a
  test that only checks the canary is absent also passes against an index holding
  no secret text at all.
- **A player sees a subset of what the DM sees, for every query in the
  language**, checked pairwise over the whole language rather than over chosen
  cases. The audience scope is one-directional, and this is what says so.
- **The FTS5 injection corpus.** FTS5 has a query language of its own and a
  search box is a second one layered on top, so a hostile query is a security
  problem before it is a relevance one. Every clause is quoted with FTS5's own
  string quoting — an inner quote doubled, which is FTS5's rule and the reason a
  naive quote breaks — and the whole expression is a bound parameter. A test
  strips the literals back out and asserts nothing but this builder's own
  operators are left, so `AND`, `NEAR/2`, `tags : "hub"`, `^toll`, `"*` and a
  hand-written column filter all arrive as words. A fuzzer runs the same
  assertion over arbitrary bytes.
- `type:` and `is:` are SQL rather than FTS5 column filters, because an FTS5
  column filter is itself a *match*: `type:homebrew-thing` would be the phrase
  "homebrew thing", and a filter that quietly means something adjacent to what was
  asked for cannot be debugged from the results. `is:` landing in the same
  `WHERE` clause as the audience scope is also what makes it a filter rather than
  a bypass.
- An **empty search box asks for nothing.** "Show me everything" is a legitimate
  question with a legitimate answer, but a search with an empty box in it is a
  request for every title in the campaign, and that list is as disclosing as the
  pages themselves. A caller that wants a listing asks for a listing.
- The four search statements are **pinned by a test that prints them**, so a
  change to how one is built shows up as a diff in the test rather than as a
  change in what a search returns, and so a reviewer can read all the SQL this
  package runs in one place.
- Excerpts come back as **plain text with no markers**. FTS5's markers would have
  to be HTML, and an excerpt of a DM's own markdown going into a response with a
  `<script>` in it is a sanitiser decision the search package should not be making
  silently. Highlighting is the view's, and it has the query terms in hand.

- **A page's audience is now recorded on its row.** M4's sync already read every
  `visibility` key, refused a value it did not recognise, and then threw the
  readable ones away — a security-relevant field validated and discarded, which
  left `visibility: dm-only` pages indexed as pages anyone could read. A read
  predicate has to filter on something, and the `visibility` column is that
  something. A blank audience is `players`, which is both the frontmatter default
  and the column default, and an audience the application does not recognise is
  refused rather than defaulted.
- **The read predicate, written once in SQL** (`internal/store/acl.go`) and used
  by both search indexes. A DM reads every page in the campaign; a player reads
  the `players` pages and nothing else; a principal with no role reads the
  `players` pages and nothing else, because a caller that forgot to look a
  principal up gets the safe answer rather than a panic.
- The ownership test is present, explicit and **false** for now (`1 = 0`),
  because the table that answers it does not exist yet. That is the fail-closed
  direction: leaving the branch out would silently widen every `dm-and-owner`
  page, and admitting every player to one would be a disclosure the moment a DM
  wrote one. A test asserts the branch is still there, which is what stops it
  being "tidied away" by somebody who has not noticed the table is missing.
- The audience test names the two levels that admit somebody and **does not name
  `dm-only` at all**, because that is the one level no clause of it may admit and
  the only way to write it down would be to exclude it — and an exclusion
  somebody can delete is not a control.
- A change to a page's audience re-indexes it. The audience is compared by name
  alongside the other derived fields rather than being left to the content hash,
  because it is a security field and a hash that happens to change when the file
  does is not the same promise.

- Two FTS5 indexes and the store methods that keep them in step with the pages
  table, so search reads a projection rather than scanning bodies: `pages_fts`
  for text nobody is barred from seeing, and `pages_secrets_fts` holding only
  `[!SECRET]` text. A page is findable by its title, aliases, tags and public
  body; a DM is additionally findable by what they wrote inside a secret.
- `pages.body_public` — the single place to look when asking "is this text safe
  to be findable?". **It is empty and stays empty until access control can
  compute it**, because the only safe value before then is the empty string: a
  body that reached the public index with a secret in it is a disclosure, and a
  body that did not is a missing feature. `wiki reindex --full` rebuilds both
  indexes from the files.
- A page's search rows are **settled, not merely written**: `PageIndexMatches`
  answers whether the index already holds what the file derives, so the sync
  engine can leave a page alone. An index that could be written but not compared
  would rot in place, and nothing in a wiki notices that for months.
- The public body is recorded on the page row as well as in the index, in the
  same transaction, so the column and the index row cannot disagree about what
  the public half was built from.
- Both index rows are part of the store contract suite, so a second `Store`
  implementation is held to the same split and the same settled check.
- The tokenizer clause in the migration is asserted by matching rather than by
  reading the schema back: `Rivergate` finds `Rivergåte`, because the failure
  mode of getting this wrong is an empty result set, which is indistinguishable
  from a page that does not exist.

- A search **query language**: bare words are ANDed, `"exact phrase"` is a
  phrase, and `tag:`, `type:` and `is:` are filters. It is a pure value with no
  database handle, so the relevance tests need no database and the parser can be
  fuzzed on its own. Whatever the language does not recognise is searched for as
  a word, because a search box that refuses input is worse than one that looks
  for a strange word.
- Every clause is quoted before it reaches FTS5 and the whole expression is
  bound as a parameter, so a query cannot become a query *language*: `AND`,
  `NOT`, `-`, `(`, `*` and a bare `"` are words, and `http://example.com` is a
  word rather than a filter on its second colon.
- `is:` is the one filter that is validated, and refusing `is:plyers` rather
  than returning nothing is deliberate: a search that finds nothing looks exactly
  like an index that has nothing to say, and a player cannot tell the two apart.
  A player who searches `is:dm-only` gets no results, not a page.
- A fuzzer for the language, which found two things worth fixing and kept both
  as seeds: a byte that is not valid UTF-8 was riding through into a clause (and
  would have made any response echoing the query invalid JSON), and a NUL inside
  a term ended it as far as the tokenizer was concerned while not ending it as
  far as the string was concerned. A filter value beginning with a colon is now
  quoted on the way out as well, so `tag: :00` survives a round trip.

### Fixed
- **An unauthenticated request could read the whole campaign.** The read
  predicate's first clause was `p.visibility = 'players'` with no mention of the
  principal, so a request that identified nobody — a session that could not be
  found, a cookie that was never sent — passed it. An empty role is not `dm`, so
  the rest of the clause never ran and every `players` page came back. The fix is
  one conjunct: `? = 'player' AND p.visibility = 'players'`.
  **`TestStoreReadPredicateMatchesResolver` found it**, in the first two of its 36
  cells, on its first run — the SQL said yes and `access.For` said no. **The
  specification writes the same clause**, so this is a correction to the spec as
  well as to the code, and it is pinned in its own test
  (`TestAnUnidentifiedRequestReadsNothing`) so that simplifying the audience test
  back to a bare visibility check fails in one place.
- **A `character:` key resolved to a page at `<slug>`** rather than at
  `characters/<slug>` — an M4 bug, which M7 surfaced by using the answer as a page
  id. Nothing could go wrong from it before: a wrong path only ever produced a "not
  a page in this campaign" report for a page whose owner was perfectly fine, which
  is noise. As a page id it is a character that is never owned, which is fail-closed
  but still a bug — a player's spell sheet would be nobody's, and the report would
  blame a page that does not exist. The spec is explicit that a character is a page
  at `characters/<slug>` and both ways of naming one resolve to the same thing.

- **A test fixture's table keys were long enough to make two Go versions
  disagree about the file.** `gofmt` aligns the values in a run of
  composite-literal entries to the widest key, and which entries count as one
  run changed in Go 1.26: a key far wider than its neighbours now goes in a
  group of its own, where 1.25 aligned everything to the widest. One
  42-character key in a table of 20-character ones was therefore a file that
  `make fmt` on Go 1.27 wrote one way and CI's `gofmt` on Go 1.25 rewrote
  another — a red build with no change to review, on the one check whose whole
  job is a byte-for-byte comparison. The keys are now of a similar length, so
  both versions agree and the table reads better for it.

- A `tag:` filter asked whichever index the query was reading for a `tags`
  column, and the private index has no `tags` column — so `tag:hub` was a SQL
  error on every secret search. Tags are a property of the *page*, and the public
  index is where a page's tags are indexed, so `tag:` is now a subquery against
  the public index in both queries. It was found by the benchmark above, which is
  the argument for having one.
- `type:`, `is:` and now `tag:` are all SQL rather than FTS5 column filters,
  because an FTS5 column filter is itself a *match*: `type:homebrew-thing` would
  be the phrase "homebrew thing", and a filter that quietly means something
  adjacent to what was asked for cannot be debugged from the results.

### Changed

- **A session now slides, and its lifetime is a config setting defaulting to 30
  days.** Every authenticated request pushes the expiry out to now plus the
  lifetime, so a player who plays every week is never asked for their link again.
  A DM who plays once a year wants a week and one who plays weekly wants a month,
  so the number is configuration rather than a constant in a package they do not
  read; a week is one line, and a DM who sets it logs their players out
  periodically on purpose.
- The trade is written down in `docs/security.md` rather than left to be
  discovered: **a leaked *cookie* can no longer be aged out by waiting, only ended
  by revoking.** A fixed window would let thirty days of doing nothing retire one
  by itself. What still ends a session immediately is a revocation, a role change
  or a binding change, and all three are row deletes rather than something the
  clock has to agree with — so a DM who suspects a cookie is in the wrong hands
  revokes, and does not wait.
- **Only a success slides.** A wrong cookie must not extend anything, or a script
  guessing session ids keeps sessions alive by trying them.
- **An expiry never moves backwards**, and the rule is enforced in the store next
  to the column rather than only in the caller that has to remember it. A clock
  that goes backwards is a machine whose battery died, and the failure is a player
  logged out mid-session with no cause.
- The slide is one `UPDATE` on a row that has just been read. The alternative —
  refreshing only when the remaining life drops below half — bounds the writes at
  the price of an effective session length that is not a number a person can state,
  which is the wrong trade for a security-relevant value.

- The migration names in the spec were wrong, and §5 now says which is which:
  `body_public` is in `0003_search` and `visibility` in `0004_visibility`, both
  earlier than the `0002_access.sql` the spec planned, and `0004_access.sql` is
  left with the owner column and the bindings. The reasons are in
  [ADR 0015](docs/adr/0015-search-records-the-audience.md): a read predicate
  filters on an audience, M4 was already reading and discarding one, and the
  public index is fed from a column that has to exist before the index does.
- [ADR 0009](docs/adr/0009-two-index-search-with-rrf.md) claimed the public
  search path has "no ACL in it at all". That is about secret *text* and it
  stands; it is not about pages, and a `dm-only` page's title is in that index.
  Both indexes now go through the read predicate, as invariant 3 names search,
  and the ADR says so rather than being left to be misread.
- The store's package comment now says which queries filter and which do not,
  rather than saying there is no access control at all. A commit that adds a
  page query must not ship with that comment removed and nothing in its place.
- The query language is written down once, in `docs/search.md`, which is where
  ADR 0009 said it would be. It is a user-facing language with sharp edges and
  reimplementing it in prose would be how it drifts.

### Added

- A `page_targets` table and three store methods, so a wiki link can resolve the
  way it does in Obsidian: the exact path, then an alias, then a case-insensitive
  file name. The aliases and the file name live in the index rather than being
  read out of `frontmatter`, because SQL cannot read YAML and a `LIKE` against
  the block would make `[[river]]` find `[[the toll on the river]]`. The table
  is entirely derived from the files and a full reindex rebuilds it.
- Two rules the lookups are explicit about, because both are ways a link
  outlives what it pointed at. An **alias is matched exactly**: case-insensitive
  alias matching would make `Rear; the Toll` and `rear; the toll` two pages with
  one reachable. A **file name is matched case-insensitively**, folded in Go
  rather than by SQLite's `LOWER()`, which only folds ASCII and would index
  `Ölbach` one way and search for it another.
- A page answers to the name of its own file from the moment its row exists:
  `UpsertPage` writes that target in the same transaction as the row. The store
  owns it because a page's name is a property of its path, and the alternative
  is every writer having to remember — a forgotten line is a link that quietly
  stops resolving.
- The lookups are part of the store contract suite, not only of the store's own
  tests, so a second implementation is held to the same rules.

- `internal/index`, and with it the resolver the renderer asked for in M3 and
  nothing had been put behind. It answers a wiki link in Obsidian's order — the
  exact path, then an alias, then a case-insensitive file name — and it is a
  **per-campaign object**, because every table in the index is campaign-scoped and
  `[[rivergate]]` means a different page in each of a DM's two campaigns.
- A sync engine: one campaign's vault read into its index, with a `Report` that
  says what it did. **It never writes a file** — that is not a limitation but
  the property ADR 0001 is about, and a test hashes every file's contents *and*
  modification time before and after a sync to keep it that way.
- A page whose frontmatter is malformed is **skipped** and the rest of the
  campaign is still indexed. A page whose `visibility` key is not one the
  application knows is **refused** and never reaches the index at all. They look
  like the same failure and are opposites: the first is a page the DM has to fix
  and nobody should wait for, the second is a page whose audience is unknown,
  and a row that exists is a row a later render may treat as `players`.
- A page with no `type:` is indexed as a `note`, the least claiming type there
  is. A DM who wrote no `type:` has not claimed a page as an NPC, and guessing
  one would give it behaviour it never asked for.
- A page is left alone when its content hash is unchanged **and** none of its
  links would resolve today. The obvious version of that test — "no unresolved
  links" — settles nothing for a page linking to a page the DM has not written
  yet, which is most pages of an early campaign: every sync would rewrite all of
  them, for ever, with the same bytes.
- One `wiki sync` is enough for a fresh vault. The walk repeats while the
  previous one wrote something, because a page indexed late in a pass is the
  target of a page the pass had already passed. A vault that is already in step
  takes one pass, and `Report.Passes` says which.
- `wiki sync` and `wiki reindex --full`, and the Makefile's `reindex` target now
  does something. `wiki sync` reads a campaign's vault into its index and takes
  that campaign's lock first; `wiki reindex --full` throws the index away and
  rebuilds it, and the flag is required, because it is work worth typing on
  purpose rather than something that happens to you. A campaign exists when its
  directory does — a vault on disk with no row in the index is drift the sync
  repairs, and importing somebody else's vault is M12's job and has a
  confirmation step of its own. An empty directory is skipped silently, because
  somebody's `Downloads` folder is not a campaign.
- `wiki sync --check` answers a question and answers it in the **exit code** as
  well as on stdout, because a script asking whether the index is in step cannot
  read stdout. It changes nothing, and a refused page counts as *not* in step.
- A watcher, so a page the DM just saved in Obsidian is the page they see when
  they reload. It watches every directory including the reserved ones (a
  directory renamed *into* the vault is one event, and a filter would throw it
  away), learns about new directories as they appear, and debounces: an Obsidian
  save is a write, a rename, a rename back and a modify, and syncing on each
  would do the same work four times and race the editor's own writes. What
  counts as a page is asked of the same `vault.CheckPagePath` the sync uses, so
  there is no second list of reserved names to drift — revisions, attachments,
  Obsidian's own directory and in-flight temporary files are all ignored.
- An archive that tolerates being done already. Two syncs — a watcher's pass
  and a `wiki sync` in the same campaign — can each have listed a page a moment
  before the other archived it, and the second one reporting an error for work
  that is already finished is an error a DM learns to ignore.
- A per-campaign lock, `internal/lockfile`, so a `wiki sync` in a terminal cannot
  interleave with the server's watcher — or with another `wiki sync`, which is
  what happens when a DM presses the up arrow. It is a file and not a row in the
  database, so holding it is holding a handle and a process that dies releases
  it. A lock that has not been refreshed inside a stale window belongs to
  something that is no longer running and is taken over, and a lock file this
  application did not write is left alone until the window passes: a file it
  cannot reason about is not one it should decide is rubbish.
- Drift detection and `ReindexFull`, the repair of last resort. **Drift is a
  state, not an error**: the projection has stopped describing what it projects,
  and ADR 0001's answer is to rebuild it from the files.
- A page is **settled** when re-deriving it from its file would produce exactly
  the row, aliases and links that are already there. One function decides that,
  and both `Sync` and `Check` ask it, so "would change" and "changed" cannot
  disagree. Settling on the content hash alone — which is cheaper, and which the
  schema's `content_hash` looks built for — is wrong in two ways that only show up
  months later: a row can rot in place with its hash still matching, and a
  milestone that changes how a body is derived leaves every row stale with hashes
  that match their files perfectly, so no incremental sync would ever repair
  them and every campaign would need a manual full rebuild.
- Ownership resolution, from docs/spec.md §8: a page is character-owned when
  its path begins with `characters/<slug>/` **or** its frontmatter declares
  `character: <slug>`, and the path wins where they disagree. The column
  (`pages.owner_character_page_id`) arrives with access control in M7; the rule
  and its validation arrive here, because the rule decides which subtree a player
  may write in and is easy to get subtly wrong and hard to notice. A
  `character:` key that normalises to a path no page has, and a page under one
  character whose key says another, are lines in the sync report — and the page is
  still indexed, because a page the DM cannot open is worse than a page with a
  wrong owner recorded.
- `Report.InStep()`: the boolean `wiki sync --check` exits on. It counts a skip
  and a **refusal** as out of step, because a page whose `visibility` cannot be
  read changes no rows when it is refused — and a check that counted only rows
  would report "in step" about a page the DM cannot see, which is the one answer
  that must never be given.
- `store.PurgePage`, the opposite of archiving: the row goes, and its revisions,
  links and name targets with it. A sync never purges; a sync archives what it
  cannot see, and only a human who has decided the row should go says so.
- A page that both embeds and links the same target is **one edge**, and the
  embed wins. The link graph's primary key is (source, destination), so a second
  row would not be stored, and the stronger statement is the more useful one.
- `render.LinkResolver` grows an error return. An index that could not answer is
  not the same answer as a target nothing answers to, and a wiki full of
  unresolved links because the database was briefly busy is a bug report about
  links that do not exist — a much worse thing to be handed than a 500.

- `internal/vault`, which reads and writes the markdown files a DM keeps in
  Obsidian. **A file the application did not change comes back out byte for
  byte.** Not semantically equal — byte for byte. A document keeps the bytes it
  was parsed from and hands them back untouched until something actually
  changes, so a reindex, a render and a save of an untouched page all produce a
  zero-byte diff, and a serialiser that tidied somebody's YAML would never put
  a diff in their git history.
- Frontmatter is a YAML parse tree, not a map. Order, comments, quote style and
  every key the application does not understand survive an edit to a key it does
  own; a `map[string]any` round trip would drop the comment beside a key and
  reorder the block on every save. Unknown keys are preserved verbatim, and the
  application refuses to write a key it does not own, because it is a guest in
  the DM's files.
- Reading is forgiving and writing is conventional, which is the only way the
  zero-byte-diff promise holds for everybody. A file with CRLF endings, a
  byte-order mark or no trailing newline is read as it is and written back as
  it is; a file the application wrote has no BOM, its own line ending and
  exactly one trailing newline. A DM on Windows is not a broken DM.
- A file whose frontmatter is not valid YAML, or is not a set of keys, or whose
  `visibility` is not a level the application knows, is **refused rather than
  interpreted** — including a typo like `plyers`. The permissive reading of a
  visibility key is how a `[!SECRET]` block reaches a player. A file with no
  frontmatter at all is not an error: most pages in a new vault have none.
- Two fuzz targets over the parser: one for "parse anything without panicking,
  and return what came in", and one for "a change to a key survives a
  re-parse, keeps the unknown keys and leaves the body alone".
- Page paths and attachment names are checked, and a checked path is not a
  string test. `vault.Open` resolves the campaign directory and every path is
  resolved against it and required to still be inside, so a symlink planted in a
  vault — a directory component *or* the file at the end of it — is a refusal
  rather than a read. A path may not contain `..`, `.`, an empty segment, a
  backslash, a volume name, a NUL, a control character, a leading dot or a
  reserved directory, and may not be a Windows device name or end in a dot or a
  space. A path that would only be valid after cleaning is refused, because
  `a/./b` and `a/b` naming two pages is a duplicate identity.
- Two more fuzz targets, one per sanitiser, whose invariant is one sentence: a
  path this package accepts names a file inside the vault, and a path it refuses
  never becomes one. After the check, the accepted path is joined to a real
  vault and required to be inside it.
- A page is written the way ADR 0001 fixes it: a temporary file in the same
  directory, `fsync`, rename, `fsync` the directory. A reader sees the old page
  or the new one, never a mixture — checked by reading while four writers write,
  and by failing a write at each of its four steps in turn. Every crash leaves
  either the old file or the new one, whole.
- Every file operation goes through an `os.Root` — a directory handle, not a
  name — so the operating system itself refuses anything that would leave the
  vault, following a symlink included. That is stronger than checking a path and
  then opening it, because there is no window between the two for a symlink to
  appear in, and it is why this package has no method that hands back an
  absolute path to open with `os.ReadFile`: a caller holding a path has left the
  guarantee behind.
- A crash leaves a temporary file behind, and opening the vault sweeps it.
  Opening is the only moment that cannot race: one data directory has one server,
  so a temporary file found then belongs to a process that is no longer running.
  Sweeping at the start of every write instead would delete a live write's
  temporary file out from under it, which is an error the caller did nothing to
  deserve.
- Revisions are archived to `_history/<path>/<n>-<rfc3339>.md`, close enough to
  Obsidian's File Recovery layout to be recognised. The timestamp has no colons
  in it, because a colon is a forbidden character in a filename on Windows and a
  revision a DM cannot open on the machine they wrote it on is not a revision.
  The number comes from the store, not from this package, so the files and
  `page_revisions` cannot disagree about the order of a page's history, and a
  page's history is a whole file rather than a diff. Revisions can be listed,
  restored, and are never listed as pages.
- Attachments live in `_attachments/` and are referred to by vault-relative
  path, so a vault stays portable when it is zipped or put in git. A page may
  point into the attachments directory — that is the case the whole thing is for —
  but not into `_history` or `.obsidian`, whatever case it spells them in.
- **The secret stripper**, which is the milestone, first among the renderer's
  features because everything else here is in service of it. A `[!SECRET]`
  callout's body is removed from the tree and the callout becomes a visible,
  obviously-empty placeholder, so a player can see that a secret is there and
  cannot see what it says. The bytes of a secret are never rendered and then
  removed, and the stripper has its own walk: goldmark's advances with
  `child.NextSibling()` after the visitor returns, and a child just spliced out
  of its parent has no next sibling left to offer, so a stripper built on it
  removes the first secret on a page and reports that it removed six.
- `internal/render`, the goldmark pipeline and the table of contents. GFM and
  footnotes are on, because a DM's notes contain tables, task lists and footnotes
  and a page whose footnotes become literal text is a page the DM has to fix by
  hand. Hard line breaks are **off**: a DM's notes are soft-wrapped, and turning
  every newline in a file into a `<br>` would break sentences at whatever column
  their editor wrapped at. The renderer version is a constant in the package,
  and it is what a page row stores and a cache key uses, so bumping it
  invalidates every render at once rather than leaving a stale cache.
- Golden files for the renderer under `internal/render/testdata/render/`,
  compared byte for byte and regenerated with `-update`. A fixture with no golden
  file beside it is a fixture that is not testing anything, so that is a failing
  test too.
- A fuzz target over the whole pipeline for the property the spec names: render
  never panics. Both decisions are exercised, and every anchor in the table of
  contents has to be an id in the HTML that came with it.
- Wiki links — `[[target]]`, `[[target|alias]]`, `[[target#heading]]` and the
  embed form `![[target]]` — as their own inline node rather than a rewrite of
  the source into markdown that happens to look like one, because a markdown
  link to a page is a page link too and a link to `https://example.invalid` is
  not. Resolution goes through a `LinkResolver` the index fills in M4, in
  Obsidian's order: exact path, then alias, then case-insensitive filename.
- An unresolved link is a `<span class="unresolved">` and not an `<a>` with no
  href: an anchor with no destination is not focusable, not clickable and not
  valid HTML, and a link's text is what the DM typed, never the resolved page's
  title. A DM writes links to pages they intend to write, and a wiki that
  rendered those as broken links would be unusable on the day it is most useful.
- Callouts: `> [!type] Title` with `-` and `+` fold markers, nesting, and the
  `{.revealed}` attribute form. A type this build has never heard of still
  renders as a callout with its type as a class, because a DM's callout
  becoming a plain blockquote is a worse answer than a callout nothing has
  styled yet — and an ordinary quote with `[!warning]` written inside it stays an
  ordinary quote.
- A fuzz target for the stripper itself: every input is a well-formed secret
  callout with a canary in it followed by whatever the fuzzer invents, and the
  canary has to be absent from the output. The trailing junk is the point — a
  fence that swallows the page, a quote that never closes, a nested callout, an
  unterminated `[!SECRET` of its own.
- A render cache keyed by `(content hash, renderer version, decision, path)`.
  Each field names what it costs to drop it: no hash serves the previous
  version of a saved page, no version keeps serving old HTML after a renderer
  upgrade, and **no decision serves a render made for a DM to a player** — a
  mistake that looks like a cache rather than like a security bug. The path is in
  there because two pages from the same template are the same bytes, and
  relative link resolution is the next thing to need it. Eviction is
  least-recently-stored rather than least-recently-used, and the code and a
  test both say which one it is rather than calling it LRU.
- A sanitiser on the way out, for **every author, the DM included**. A
  sanitiser applied only to player-authored markdown leaves the highest-value
  target in the application on the weakest path: a DM pastes a snippet from a
  forum into their own notes, and a forum is a place scripts come from. The
  policy is built from what this renderer emits rather than from what HTML can
  do, and `style`, `data-*`, comments, forms, iframes and `unsafe` are all
  absent. `class` is allowed as a space-separated list of this application's own
  classes, so a DM cannot reach a stylesheet rule that is not theirs.
  The corpus found two holes in the first draft of that policy — a second,
  unconstrained allowance of `class` beat the matched one, and a pattern that
  matched a single class name stripped the attribute from every callout — and a
  review of the golden diff found a third: `blockquote` was missing, which
  turned a GM's rulebook quote into an unattributed paragraph.
- ~75 XSS corpus payloads run through the whole pipeline, the way a DM pastes
  something: script and event handlers, `javascript:` in five spellings, svg,
  data URLs, style expressions, form and frame tricks, and the payloads that
  hide behind our own class names. Every one is checked for the *absence* of the
  construct in the output, and the prose after it has to survive, so a payload
  cannot take the page with it.

- `wiki migrate`, for the two questions a person has about a database: what
  schema is it at, and bring it to the one this build knows about. It prints
  the path it touched every time, because a migration command that only says
  "migrated" gets run twice in the wrong directory, and `-status` answers
  without writing anything — including without creating an empty database where
  there was none, and saying so loudly if the path is not one you meant.
- `internal/datadir`, so every command that touches a database agrees on where
  the data directory is: `-data-dir` beats `DDSP_DATA_DIR`, which beats the
  platform default. That is ADR 0011's precedence in three rules, and
  `config.yaml` is not read yet — a half-implemented config loader that
  silently ignored a config file a DM had written would be worse than one that
  does not exist.

- `internal/clock` and `internal/idgen`, the only two packages allowed to know
  what time it is and what a new identifier looks like. Anything that stamps a
  row or mints a key takes a `clock.Clock` or an `idgen.IDGen` instead, so a
  test can hand it a sequence it can predict and assert on every id in a
  golden file. Production gets the system clock and random UUIDs;
  `clock.Fixed` steps forward by a fixed interval on every call, so
  consecutive writes get distinct and ordered timestamps rather than one
  repeated value.
- `internal/domain`, the values the application is about: campaigns, pages,
  revisions, links, principals, sessions and audit entries, with the three
  `Slug`, `Role`, `Visibility` and `LinkKind` vocabularies they use. Every
  value can check itself, so a bad row is refused at the boundary with an
  error naming the field instead of surfacing as `UNIQUE constraint failed`.
  Two consequences worth knowing: a page's path is its identity, so retitling
  never moves a file; and a page is required to carry a content hash, because
  a row that cannot be compared to a file is reindexed on every run. Campaign
  slugs are normalised rather than rejected, so `The Blackwater` becomes
  `the-blackwater`, and a slug can never come out as `..`, absolute or
  containing a path separator — pinned by a property test and a fuzz target.
- `migrations/0001_init`, the base schema: campaigns, pages, revisions, the
  link graph, principals, sessions and the audit log, with the indexes that
  make backlinks and "active now" cheap. The SQL is embedded in the binary
  rather than shipped beside it, which is what "one binary and a data
  directory" means in practice.
- A migration runner that takes a database to a known version or says why it
  cannot. It records one row per applied version, refuses a migration set with
  a hole in it or a version with no way back, stops against a database written
  by a newer build, and marks a version dirty *before* running its SQL — so a
  migration that fails half way stops the next run instead of being retried on
  top of a schema nobody described. `Force` is the documented way out of that
  state, and it is deliberately blunt.
- Timestamps are written as RFC 3339 with a fixed nine-digit fraction, because
  a timestamp column is TEXT: with a variable-width fraction, `…T19:03:00.4Z`
  sorts after `…T19:03:00.45Z` and every `WHERE created_at < ?` in the
  codebase would be subtly wrong. There is a test that asserts the two orders
  agree.
- `store.Open`, which opens a campaign database the way ADR 0004 describes and
  then **verifies** it: if WAL, foreign keys, the busy timeout or the
  synchronous setting did not take, the store refuses to open rather than
  running with a referential-integrity guarantee it does not have. A test asks
  for more connections than queries, on purpose, because a pragma set once is
  a pragma set on one connection.
- Reads and writes go to separate pools over the same file, with a single write
  connection. Two writers cannot collide, so `SQLITE_BUSY` between them cannot
  happen at all, and a reader never queues behind a write.
- A value a caller leaves zero is filled in: the store mints the id and stamps
  the timestamps from the injected clock, then returns the row as stored. A
  database path containing `?` or `#` is refused, because the connection string
  would be truncated at that character and the database would be created
  somewhere else.
- Store methods for campaigns, pages, revisions and the link graph. Every one
  of them fills in what the caller left blank — an id from the generator, a
  timestamp from the clock — and returns the row **as stored** rather than the
  value it was handed, so a test can assert on every field of a result.
  Archiving a page keeps the row: revisions and inbound links point at it, and
  an archive is recoverable. Listing is ordered by slug or by path, because a
  list whose order varies between runs cannot be diffed.
- Storing a page twice at the same path updates the row and keeps its id and
  creation time, because a page's identity is its path and its id is what every
  revision and every inbound link points at. Renaming a page is a different
  operation that rewrites those links, and it is not this method.
- `ReplaceLinks` makes a page's outgoing links be *exactly* the links given, in
  one transaction, including the empty case. Appending would leave links to
  pages the DM deleted from the text, and a link graph that remembers removed
  links is a graph nobody can trust.
- Unique and primary key violations come back as `store.ErrConflict`, a missing
  row as `store.ErrNotFound`, and a driver result code is read through a small
  interface rather than by matching on message text.
- A store contract suite in `internal/store/testsuite`, run against the SQLite
  store as `testsuite.Store(t, factory)`. It is where the properties a second
  implementation would have to match live: a value written comes back unchanged,
  blank ids and timestamps are filled in, writing the same value twice changes
  nothing, lists are ordered and complete, an archived row is invisible but
  still there, a path in two campaigns is two pages, a taken slug is a
  conflict, a missing row is not found, a row that references nothing is
  refused, revision numbers start at one per page, replacing links is all or
  nothing, and the graph holds links to pages that do not exist yet. The suite
  declares the interface it needs itself, so a second implementation is pointed
  at it rather than trusted.
- `wiki` command with `version` and `help` subcommands. The usage text is
  generated from the subcommand table, so a subcommand cannot exist without
  appearing in `wiki help`.
- `internal/version`, which reports the build version, commit, build date and
  Go toolchain. Every field is set with `-ldflags` at link time and defaults to
  `dev` and `unknown` so an unlinked build never claims to be a release.
  `wiki version` prints it; `/healthz` will report the same struct in M8.
- `.golangci.yml`, pinning the linter set: `errcheck`, `gosec` and
  `errorlint` for the access-control and error-handling invariants, plus
  formatting checks on `gofmt` and `goimports`.
- `wiki` returns an exit code of 0 on success and 1 on failure, decided by
  `exitCode` rather than by `os.Exit`, so the mapping is table-tested.

### Fixed

- Two `internal/vault` tests compared things the operating system decides, and
  so passed on Linux and failed on the CI matrix: one compared a *resolved*
  path against the unresolved one it was given (identical on Linux, different
  on macOS, where a temporary directory is reached through `/var`, and on
  Windows, where the runner's home directory has both a long and an 8.3
  spelling of itself), and one asserted an error message that Linux produces
  from `EvalSymlinks` and Windows produces from `MkdirAll` as two different
  correct refusals. The first now compares like with like; the second asserts
  what it is about — that a failed write leaves no temporary file behind — and
  says why the message is not the test's business.
- A `vault.Vault` that was never closed leaked its directory handle, which on
  Windows is opened without `FILE_SHARE_DELETE` — so the handle stopped the
  temporary directory from being deleted at the end of the test, with a message
  about a file being in use by another process. The test helpers close what they
  open now, the concurrent write-and-read test waits for its writers in a
  cleanup so a failure cannot leave them writing into a directory that is being
  removed, and `Vault.Close` says why a caller has to mean it.
- A write to a closed stdout or stderr is now reported to the caller instead
  of being discarded. Previously `wiki help` could print a truncated page and
  exit 0.
- Pressing Ctrl-C no longer risks leaving the signal handler installed:
  `os.Exit` skipped the deferred cleanup, so `stop` is now called explicitly.

### Development

- A `.gitattributes` pinning every text file to LF on every platform. The
  reason is the renderer's golden files: they are compared byte for byte, so
  their line endings are part of what they assert, and a Windows checkout with
  git's default `core.autocrlf=true` rewrote them to CRLF — which fails the test
  over a difference that `git diff`, a terminal and a reviewer all render as
  nothing. A golden file test that can fail without a visible cause teaches a
  maintainer that a green-looking diff is not a green test. `text=auto` leaves
  binaries alone, and no file in this repository wants CRLF.

- `modernc.org/sqlite` moved to v1.59.0 (SQLite 3.53.4) and the module's Go
  directive to 1.25.0, which is the newest Go that driver family needs and the
  newest Go the driver can be used from. v1.47.0 and later declare `go 1.25.0`,
  so holding the directive at 1.24 meant holding the driver two years back.
  FTS5, `snippet()`, `bm25()`, the DSN pragmas and `CGO_ENABLED=0` are all
  re-verified on v1.59.0, because M5's two-index search is built on them.
- The coverage gate now measures the whole module rather than each package's
  own test binary. A package that only ever runs as part of another package's
  tests — `internal/store/testsuite` is the first, and it is most of the M1
  suite — used to report 0% and fail a gate the code was passing all day.
  Per-package numbers in `make cover` output are now "share of the module
  covered by this package's tests"; the gate and `coverage.out` are
  unaffected.
- `Makefile` with `check`, `test`, `cover`, `fuzz`, `lint`, `vet`, `fmt`,
  `build`, `run`, `reindex`, `spike` and `install-tools`. `make help` lists
  them.
- Pinned tool versions live in the `Makefile` and nowhere else, and CI reads
  them from there with `make print-<tool>-version`, so a version bump is one
  edit rather than three.
- `make cover` fails below the 80% coverage floor. The floor is one
  `COVERAGE_MIN` assignment, not a number typed into a workflow.
- GitHub Actions workflow with six jobs: `lint` (gofmt, vet,
  golangci-lint), `test` (Linux, macOS and Windows, which is what keeps
  "no CGO" true), `coverage` (the 80% floor), `build` (compile, run
  `wiki version`, and check the binary is static), `changelog` and
  `spike`. Every job runs a `make` target, so CI and a local machine
  execute the same command. Actions are pinned to commit SHAs.
- `.gitmessage` commit template, documenting the types, the scopes and
  the changelog rule.
- `scripts/check-changelog.sh`, which fails a push that changed
  `internal/`, `cmd/`, `plugins/`, `migrations/` or `web/` without
  changing `CHANGELOG.md` in the same commit. It reports the offending
  commits by short SHA and subject rather than just saying no.
- A Datastar spike under `spike/`, implementing ADR 0006's four-function
  SSE interface twice — once on `datastar-go`, once with only
  `net/http` — and asserting the two agree on the wire. It is a separate
  Go module, so it can be run and re-run without putting Datastar in
  the application's dependency graph. Findings in ADR 0008.

### Documentation

- ADR 0013 records why a document is the bytes it was read as: ADR 0005 asks
  for unknown keys preserved verbatim *and* a zero-byte diff on rewriting an
  untouched file, and together those are sharper than they read — no YAML
  emitter agrees byte for byte with every hand-written file. The promise
  cannot be tested for, so it is structural: a document keeps its bytes until
  something actually changes, and the frontmatter is a parse tree rather than a
  map, because a map drops key order, comments and quote style.
- ADR 0014 records where a secret leaves the render path and what the render
  cache is keyed by. The stripping happens on the parse tree, so a secret's
  text is never rendered and then removed, and the cache key carries the
  decision, so a render made for a DM cannot be served to a player.
  `docs/spec.md` §11 is corrected: its two-field cache key is a channel.
- ADR 0012 is corrected. It originally claimed golang-migrate had no pure-Go
  SQLite driver and that its only one was a cgo binding, which ruled the
  library out under ADR 0004. It has one: `database/sqlite` imports
  `modernc.org/sqlite` and takes the same `*sql.DB` this store already builds.
  The decision to keep an in-repo runner stands, but on the corrected grounds —
  it is already written and tested, it keeps a row per applied version, and it
  is a close call that should be revisited if the runner has to grow — not on a
  blocked door. `docs/spec.md` §3 and its decision table are corrected with it.
- ADR 0008 records the Datastar pins from that spike: client v1.0.4, server
  `datastar-go` v1.2.2, the four-method mapping onto the SDK, the actual wire
  format, and the two defects the spike turned up. It closes the "known risk"
  in `docs/spec.md` §3.
- ADR 0009 records why search uses two FTS5 indexes merged with Reciprocal
  Rank Fusion. A page row and the text inside it can have different readers, so
  filtering by page and showing a snippet is already a disclosure: the match
  position carries the secret. It also fixes `is:dm-only` as a filter applied
  inside the ACL join rather than a bypass, and extends
  `pages.renderer_version` to mean "the renderer version this index was built
  against".
- ADR 0010 records where the line between core and a plugin falls: core owns
  the eight ruleset-agnostic page types, ownership, `[!SECRET]` and the ACL; a
  page type is data rather than a Go type; and a plugin may not add a
  visibility level, register a second render path or FTS index, or read the
  database unfiltered.
- ADR 0011 records the deployment shape: one static binary, one data
  directory, and "copy the directory" as the whole backup procedure. It fixes
  the two things the spec left undecided — one server per data directory via
  `locks/serve.lock`, and environment-over-file configuration precedence.
- `docs/security.md`, the threat model ADR 0003 has referenced since it was
  written. Assets in priority order, the threat actors, every control mapped
  to the named test that enforces it, and six accepted limitations stated
  rather than discovered.

### Development

- **The Go toolchain that formats the code is now pinned in the `Makefile`**, and
  `make fmt` and `make fmt-check` run that toolchain's `gofmt` rather than
  whatever is on `PATH`. Every other tool was already pinned this way; the one
  that decides whether CI is green was not, and that is how the build failed — a
  `gofmt` from the developer's Go rewriting a file CI's `gofmt` had just
  accepted. `gofmt` may change its output in any release, on purpose, so this
  cannot be left to chance when the check is a byte-for-byte comparison. It is
  reached with `GOTOOLCHAIN` and `go env GOROOT`, because `gofmt` is a separate
  binary from the `go` command and `GOTOOLCHAIN` does not reach the one on
  `PATH`, and it is resolved inside the recipe so that `make help` does not
  download a toolchain.
- `make check-go-version` fails if the pin and the `go` line in `go.mod` drift
  apart, because CI installs Go from `go.mod` and formats with the pin: a drift
  is `fmt-check` failing on a file nobody changed, reached by the very mechanism
  meant to prevent it. `fmt-check` depends on it, so any formatting check catches
  it.

## [0.1.0] - TBD

_First release. Will cover milestones M0 through M12; see `docs/spec.md`
§ Milestones for the breakdown._

[Unreleased]: https://github.com/popinjayjohn/dine-and-dash-semiplane/compare/v0.1.0...HEAD
