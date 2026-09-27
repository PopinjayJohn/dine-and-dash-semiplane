package vault

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// frontmatter is one YAML block, kept as the tree it parsed into.
//
// The tree is the point. A map[string]any would drop the order the DM wrote
// the keys in, the comments they wrote beside them, the difference between
// 'single' and "double" quotes and the exact spelling of a timestamp; a
// re-serialisation from it would then be a rewrite of somebody's notes on
// every save. The tree keeps all of it, so the only thing a change to a key
// costs is the whitespace between keys.
type frontmatter struct {
	// node is the document node, exactly as parsed or last modified.
	node yaml.Node

	// raw is the block as it was read, returned unchanged while the document
	// is unmodified.
	raw string

	// dirty is set by Set and Remove. It is the only reason text ever
	// re-serialises.
	dirty bool
}

// text renders the block: the bytes it was read as, or the deterministic
// serialisation of the modified tree.
func (f *frontmatter) text() (string, error) {
	if !f.dirty {
		return f.raw, nil
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	// Two spaces, because that is what Obsidian writes and what every
	// frontmatter block in an existing vault already uses.
	encoder.SetIndent(2)

	if err := encoder.Encode(&f.node); err != nil {
		return "", fmt.Errorf("%w: re-serialising frontmatter: %w", ErrFrontmatter, err)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("%w: re-serialising frontmatter: %w", ErrFrontmatter, err)
	}

	return buf.String(), nil
}

// empty reports whether the block has no keys at all, which is a different
// thing from having no block: an empty block still needs its two fences.
func (f *frontmatter) empty() bool {
	mapping, err := f.mapping()
	if err != nil || len(mapping.Content) == 0 {
		return true
	}
	return false
}

// mapping returns the top-level mapping, or an error naming what the block
// holds instead. A frontmatter block that is a list, or a scalar, is a file to
// ask the DM about rather than to interpret.
func (f *frontmatter) mapping() (*yaml.Node, error) {
	if len(f.node.Content) == 0 {
		return nil, fmt.Errorf("%w: the block is empty", ErrFrontmatter)
	}

	root := f.node.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: frontmatter is a %s, which is not a set of keys", ErrFrontmatter, kindName(root))
	}
	return root, nil
}

// mappingOrCreate returns the top-level mapping, creating an empty one when
// the document had no block at all. Setting a key on a file with no
// frontmatter adds the block rather than refusing: a DM who writes prose and
// then renames the file in the app should get a title, not an error.
func (f *frontmatter) mappingOrCreate() (*yaml.Node, error) {
	if len(f.node.Content) == 0 {
		f.node = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
		}
	}
	return f.mapping()
}

// value returns the decoded value of a key, whether it is present, and any
// error in its shape.
//
// Decoding happens per lookup rather than once at parse time, so a document
// that is modified between two reads cannot hand out a value from before the
// change. A vault is a few thousand pages; walking a handful of keys is not
// worth a cache that can be stale.
func (f *frontmatter) value(key Key) (any, bool, error) {
	if f == nil || len(f.node.Content) == 0 {
		return nil, false, nil
	}

	mapping, err := f.mapping()
	if err != nil {
		return nil, false, err
	}

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		name, value := mapping.Content[i], mapping.Content[i+1]
		if name.Kind != yaml.ScalarNode || name.Value != string(key) {
			continue
		}
		return decodeValue(value)
	}

	return nil, false, nil
}

// set replaces a key's value, or appends the key. The key node is kept when it
// exists, so a comment written beside it survives the change.
func (f *frontmatter) set(key Key, value any) error {
	encoded, err := encodeValue(keyKinds[key], value)
	if err != nil {
		return err
	}

	mapping, err := f.mappingOrCreate()
	if err != nil {
		return err
	}

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		name := mapping.Content[i]
		if name.Kind != yaml.ScalarNode || name.Value != string(key) {
			continue
		}
		mapping.Content[i+1] = encoded
		f.dirty = true
		return nil
	}

	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(key)},
		encoded,
	)
	f.dirty = true

	return nil
}

// remove deletes a key, and reports whether it was there. Removing a key the
// document does not have is not an error: the caller wanted it gone and it is.
func (f *frontmatter) remove(key Key) (bool, error) {
	if f == nil || len(f.node.Content) == 0 {
		return false, nil
	}

	mapping, err := f.mapping()
	if err != nil {
		return false, err
	}

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		name := mapping.Content[i]
		if name.Kind != yaml.ScalarNode || name.Value != string(key) {
			continue
		}
		mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
		f.dirty = true
		return true, nil
	}

	return false, nil
}

// decodeValue turns a value node into a Go value: a scalar as the string the
// DM typed, a sequence of scalars as a list of them, a null as the empty
// string, and anything else as an error.
//
// Scalars are strings and not whatever YAML would infer. `title: 42` and
// `title: '42'` are the same title to a human, and a page title that becomes
// the number 42 on the way into the database is a bug in the wrong direction.
func decodeValue(node *yaml.Node) (any, bool, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Value, true, nil
	case yaml.SequenceNode:
		items := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, true, fmt.Errorf("%w: a list of %s, which is not a list of words",
					ErrFrontmatter, kindName(item))
			}
			items = append(items, item.Value)
		}
		return items, true, nil
	case yaml.MappingNode:
		return nil, true, fmt.Errorf("%w: a set of keys, which is not a value the application reads", ErrFrontmatter)
	case yaml.AliasNode:
		return node.Value, true, nil
	default:
		return nil, true, fmt.Errorf("%w: a %s, which the application does not read", ErrFrontmatter, kindName(node))
	}
}

// kindName describes a node for an error message a DM can act on.
func kindName(node *yaml.Node) string {
	switch node.Kind {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "list"
	case yaml.MappingNode:
		return "set of keys"
	case yaml.ScalarNode:
		return "word"
	case yaml.AliasNode:
		return "reference"
	default:
		return "unknown value"
	}
}

// Key is a frontmatter key the application owns.
//
// The list is closed on purpose. The application is a guest in the DM's files
// (ADR 0005): it reads the keys it needs and writes them back, and it does not
// invent keys, rename them or remove ones it does not understand. A Set of a
// key that is not one of these is refused, so a future bug cannot quietly write
// "titel: Rivergate" into somebody's campaign.
type Key string

// The keys the application owns, from the frontmatter example in
// docs/spec.md §6 plus the character key of §8.
const (
	KeyTitle      Key = "title"
	KeyAliases    Key = "aliases"
	KeyTags       Key = "tags"
	KeyType       Key = "type"
	KeyVisibility Key = "visibility"
	KeyCreated    Key = "created"
	KeyUpdated    Key = "updated"
	KeyCharacter  Key = "character"
)

// keyKinds is what shape each key's value has. It exists so that setting
// `tags: rivergate` is an error rather than a tag list containing one string
// that happens to be spelled wrong.
var keyKinds = map[Key]keyKind{
	KeyTitle:      kindScalar,
	KeyAliases:    kindList,
	KeyTags:       kindList,
	KeyType:       kindScalar,
	KeyVisibility: kindScalar,
	KeyCreated:    kindTimestamp,
	KeyUpdated:    kindTimestamp,
	KeyCharacter:  kindScalar,
}

// encodeValue builds the value node for a key.
//
// A timestamp is tagged rather than quoted. The YAML encoder quotes any string
// that would otherwise read as a timestamp, which is correct YAML and the wrong
// shape for this project: the spec's example writes `created: 2026-02-14T19:03:00Z`
// plain, every tool that reads a vault expects that, and a quoted timestamp in
// a DM's file is a diff they did not ask for.
func encodeValue(kind keyKind, value any) (*yaml.Node, error) {
	if kind == kindTimestamp {
		stamp, err := timeValue(value)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrKey, err)
		}

		return &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!timestamp",
			Value: stamp.UTC().Format(Timestamp),
		}, nil
	}

	var encoded yaml.Node
	if err := encoded.Encode(value); err != nil {
		return nil, fmt.Errorf("%w: %T cannot be stored: %w", ErrKey, value, err)
	}
	return &encoded, nil
}

// timeValue accepts a time or a string that is one, and returns the time in
// UTC. A string is accepted because a caller restoring a revision has the text
// the file had, not a time it parsed correctly first.
func timeValue(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case string:
		parsed, err := parseTime(typed)
		if err != nil {
			return time.Time{}, err
		}
		return parsed, nil
	default:
		return time.Time{}, fmt.Errorf("a timestamp is a time or a string, not a %T", value)
	}
}

// textValue extracts the string a value spells, whether it is a string or a
// defined type over one.
//
// A caller setting `type` naturally has a domain.PageType in hand, and a
// domain.Visibility for `visibility`. Requiring four call sites to convert
// those to plain strings is four chances to forget, and the value the file
// ends up with is the same either way.
func textValue(value any) (string, bool) {
	if value == nil {
		return "", false
	}

	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.String {
		return reflected.String(), true
	}
	return "", false
}

// keyKind is the shape a key's value has to have.
type keyKind int

const (
	kindScalar keyKind = iota
	kindList
	kindTimestamp
)

// Valid reports whether k is a key the application owns.
func (k Key) Valid() bool {
	_, owned := keyKinds[k]
	return owned
}

// String returns the key as it appears in the file.
func (k Key) String() string {
	return string(k)
}

// ownedKeys returns the keys in the order they are declared, for the help text
// and for a test that walks them.
func ownedKeys() []Key {
	return []Key{
		KeyTitle, KeyAliases, KeyTags, KeyType,
		KeyVisibility, KeyCreated, KeyUpdated, KeyCharacter,
	}
}

// CoreKeys returns every key the application owns, in declaration order.
//
// It exists so that a second package can ask the same question without keeping its
// own copy of the answer. `internal/plugin` refuses a plugin that claims one of
// these, and a list over there is a list that goes stale the first time a key is
// added here — which is how a plugin ends up redefining `visibility:` while the
// code that defines it believes it cannot.
//
// `TestOwnedKeys` in this package is the other half: it already holds `ownedKeys`
// against `keyKinds`, so the list cannot lose a key without a test noticing.
func CoreKeys() []Key {
	return slices.Clone(ownedKeys())
}

// checkValue reports whether a value can be stored under a key, so that
// `tags: rivergate` is refused at the call rather than written as a tag list
// containing one string that happens to be spelled wrong.
func checkValue(key Key, value any) error {
	kind, owned := keyKinds[key]
	if !owned {
		return fmt.Errorf("%w: %q", ErrKey, key)
	}

	switch kind {
	case kindTimestamp:
		switch typed := value.(type) {
		case time.Time:
			return nil
		case string:
			if _, err := parseTime(typed); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrKey, key, err)
			}
			return nil
		default:
			return fmt.Errorf("%w: %s holds a timestamp, not a %T", ErrKey, key, value)
		}
	case kindScalar:
		text, isText := textValue(value)
		switch {
		case isText && strings.ContainsAny(text, "\n"):
			return fmt.Errorf("%w: %s cannot hold a value spanning lines", ErrKey, key)
		case isText:
			return nil
		case value == nil:
			return fmt.Errorf("%w: %s holds one value; pass Remove to clear it", ErrKey, key)
		default:
			return fmt.Errorf("%w: %s holds one value, not a %T", ErrKey, key, value)
		}
	case kindList:
		switch typed := value.(type) {
		case []string:
			for _, item := range typed {
				if strings.ContainsAny(item, "\n") {
					return fmt.Errorf("%w: %s cannot hold a value spanning lines", ErrKey, key)
				}
			}
			return nil
		case nil:
			return fmt.Errorf("%w: %s holds a list; pass an empty list to clear it", ErrKey, key)
		default:
			return fmt.Errorf("%w: %s holds a list, not a %T", ErrKey, key, value)
		}
	default:
		return fmt.Errorf("%w: %q has no defined shape", ErrKey, key)
	}
}
