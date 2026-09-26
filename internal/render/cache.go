package render

import (
	"container/list"
	"sync"
)

// The render cache is the one place in this package where a mistake hands a
// secret to somebody who may not read one, so its key is the thing to look at.
//
// A key is (content hash, renderer version, decision, path). Drop any of them
// and something breaks that does not look like a security problem:
//
//   - without the content hash, a saved page serves the previous version;
//   - without the renderer version, a renderer upgrade keeps serving the old
//     HTML for every cached page, which is the failure ADR 0009's whole
//     constant exists to prevent;
//   - without the decision, **a render made for a DM is served to a player**.
//     That is the one. It is why the decision is in the key and why a cache
//     miss is always safe and a cache *hit* has to be earned.
//
// The path is in the key too, so that two pages with identical bodies -- two
// stub pages a DM created from the same template -- get their own entries and
// their links resolve against the right place. It costs a few bytes and it
// removes a class of bug where a stub page inherits another page's resolved
// links.

// CacheKey identifies one render.
type CacheKey struct {
	// ContentHash is the hash of the file the body came from.
	ContentHash string

	// Version is the renderer version that produced the entry.
	Version Version

	// CanSeeSecrets is the decision the entry was made under.
	CanSeeSecrets bool

	// Path is the page the entry belongs to.
	Path string
}

// Cache is a bounded map from a key to a render.
//
// It is an LRU rather than a plain map with a counter because a campaign is
// read unevenly: one page is read a hundred times a session and the rest once
// each, and the bound should cost the *cold* pages rather than the hot one.
//
// Every method is safe for concurrent use, and Get does not update the
// recency: a cache is read far more often than it is written, and making a read
// take a write lock would make the hot path the contended one.
type Cache struct {
	mu    sync.Mutex
	limit int
	order *list.List
	items map[CacheKey]*list.Element
}

// entry is what the list holds: the key and the value, so a touch can find
// both from one allocation.
type entry struct {
	key    CacheKey
	result Result
}

// defaultCacheSize is how many renders are kept.
//
// A campaign is a few thousand pages and this application is one DM and a
// handful of players, so the working set is the pages being read this session
// rather than the whole campaign. Five hundred renders is more than that and is
// small enough to be a rounding error in a process whose memory is mostly
// markdown.
const defaultCacheSize = 500

// NewCache returns a cache holding at most limit entries. A limit of zero or
// less means no caching at all, which is a thing a test may want and a
// production build should not.
func NewCache(limit int) *Cache {
	if limit < 0 {
		limit = 0
	}

	return &Cache{
		limit: limit,
		order: list.New(),
		items: make(map[CacheKey]*list.Element, limit),
	}
}

// Get returns a cached render, if there is one.
func (c *Cache) Get(key CacheKey) (Result, bool) {
	if c == nil || c.limit == 0 {
		return Result{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	element, found := c.items[key]
	if !found {
		return Result{}, false
	}

	stored, isEntry := element.Value.(*entry)
	if !isEntry {
		// Unreachable: the list holds nothing else. Said rather than asserted,
		// because a cache that returns somebody else's zero value when it is
		// confused is worse than one that misses.
		return Result{}, false
	}

	// The hit does not update the recency. A DM reading one page for an hour
	// would otherwise push every other page out of the cache by reading it,
	// which is the opposite of what the bound is for.
	return stored.result, true
}

// Put stores a render, evicting the least recently stored entry if the cache is
// full.
//
// "Least recently stored" rather than "least recently used" follows from Get
// not updating recency: the list is in insertion order, so the front is the
// oldest entry and evicting it is one pop. A stricter LRU would be better for a
// workload that reads unevenly, and this is the honest version of it for now.
func (c *Cache) Put(key CacheKey, result Result) {
	if c == nil || c.limit == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if element, found := c.items[key]; found {
		if stored, isEntry := element.Value.(*entry); isEntry {
			stored.result = result
			c.order.MoveToBack(element)
		}
		return
	}

	c.items[key] = c.order.PushBack(&entry{key: key, result: result})

	for c.order.Len() > c.limit {
		oldest := c.order.Front()
		if oldest == nil {
			return
		}
		c.order.Remove(oldest)

		if evicted, isEntry := oldest.Value.(*entry); isEntry {
			delete(c.items, evicted.key)
		}
	}
}

// Len is how many renders the cache is holding, for a test and for the
// /healthz line.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.order.Len()
}

// Clear empties the cache. Nothing needs it at run time, and a thing with no
// caller is a thing to delete rather than keep.
func (c *Cache) Clear() {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[CacheKey]*list.Element, c.limit)
	c.order.Init()
}
