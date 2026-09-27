package vault

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// The plugin seam: reading a key the application does not own.
//
// ADR 0013 closed the set of frontmatter keys, and the reason it gives is worth
// quoting rather than paraphrasing because this file is the one place the
// application steps around the closure, and a reader has to be able to check that
// the reason still applies:
//
//   > The keys the application owns are a closed set of eight, and `Set` refuses
//   > anything outside it, because the application is a guest in the DM's files
//   > (ADR 0005) and a bug that writes `titel: Rivergate` into somebody's campaign
//   > is not a bug that should be able to reach the filesystem.
//
// Two things follow, and they are the whole of the argument:
//
//   - **The closure is on the way out, not on the way in.** `Set` is the operation
//     that reaches the filesystem and it is still closed. Nothing in this file can
//     write.
//   - **A plugin's key is not a bug.** It is a key a compile-time list vouched for:
//     `internal/plugin`'s `AddFieldType` refuses a core key, so the one way a plugin
//     could shadow `visibility:` is refused at startup rather than caught here.
//
// So the seam is a *read*, and a read has none of the properties ADR 0013 was
// protecting. The parse tree is untouched: this builds a throwaway `yaml.Node` from
// the projection and throws it away, so the zero-byte-diff promise is not even in
// play.

// Fields is the value of each of the named keys in a frontmatter block, and the
// order the file wrote them in.
//
// It takes the **frontmatter text**, not a [Document], because the render path has
// the text: a page's row carries `pages.frontmatter` and the editor has the file's
// bytes, and neither of them has a parsed document. A function that wanted a
// `*Document` would be a function the two callers could not call.
//
// The order is returned rather than left to the map because a page whose fields
// rearrange themselves between builds is a page nobody can screenshot, and a map is
// unordered by construction. The two answers come from one walk of one node, so
// asking for both cannot disagree about which keys the file has.
//
// A key the block does not have is in neither answer. A page whose DM has not filled
// in `school:` yet has no `school` field to render, and a renderer that cannot tell
// "absent" from "blank" renders a row of nothing for every spell somebody has not
// finished.
//
// A key whose value is a list is joined with a space rather than refused, because
// `components: v, s, m` is the obvious way for a DM to write three values and a
// plugin that wanted three would rather have "v s m" than an error. A key whose value
// is a mapping is skipped entirely: there is no sensible flattening of a nested map
// into a string, and a plugin that wants structure wants a different capability.
func Fields(frontmatter string, keys []Key) (values map[Key]string, order []Key) {
	if strings.TrimSpace(frontmatter) == "" || len(keys) == 0 {
		return nil, nil
	}

	mapping, err := decodeMapping(frontmatter)
	if err != nil || mapping == nil {
		// A frontmatter block that is not a mapping is a page whose `Parse` already
		// refused, so for a row that exists this is unreachable. The empty answer is
		// the safe one: no fields rendered rather than fields guessed at.
		return nil, nil
	}

	// The claims are normalised on the way in, and the file's keys on the way out,
	// so a claim on `casting-time` finds `casting_time`. See [NormaliseFieldKey]: a
	// YAML key is not a slug and a DM writes `casting_time`.
	wanted := make(map[string]Key, len(keys))
	for _, key := range keys {
		if normalised := NormaliseFieldKey(string(key)); normalised != "" {
			wanted[normalised] = key
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}

	values = make(map[Key]string, len(wanted))
	order = make([]Key, 0, len(wanted))
	seen := make(map[Key]bool, len(wanted))

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		name, value := mapping.Content[i], mapping.Content[i+1]
		if name.Kind != yaml.ScalarNode {
			continue
		}
		key, asked := wanted[NormaliseFieldKey(name.Value)]
		if !asked {
			continue
		}
		text, isText := scalarText(value)
		if !isText {
			continue
		}
		if !seen[key] {
			seen[key] = true
			order = append(order, key)
		}
		// A repeated key is a YAML error a DM's editor would have caught, and the
		// *last* one is what most YAML readers use. Matching that is the least
		// surprising answer, and the order records the first position so the field
		// appears where the DM would look for it.
		values[key] = text
	}

	if len(values) == 0 {
		return nil, nil
	}
	return values, order
}

// decodeMapping is a frontmatter block's top-level mapping.
//
// It is `yaml.Unmarshal` into a `yaml.Node` rather than into a `map[string]any` on
// purpose: a map drops the key order this function's second answer is made of, and
// it drops it before the walk starts rather than at the end.
func decodeMapping(frontmatter string) (*yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(frontmatter), &document); err != nil {
		return nil, err
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) == 0 {
		return nil, nil
	}

	mapping := document.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil, nil
	}
	return mapping, nil
}

// scalarText is a scalar node's text, or a list's items joined with a space.
//
// A `!!null` scalar is the empty string, which is the same answer a DM writing
// `school:` with nothing after it meant: the key is there and the value is not.
func scalarText(node *yaml.Node) (string, bool) {
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Value, true
	case yaml.SequenceNode:
		items := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind == yaml.ScalarNode {
				items = append(items, item.Value)
			}
		}
		return strings.Join(items, " "), true
	default:
		return "", false
	}
}

// NormaliseFieldKey is the one spelling a claimed frontmatter key and the key a DM
// wrote are compared as: lower case, with `_`, `.` and spaces folded to `-`.
//
// It is a function and not a rule in the comment because both sides have to apply
// it, and two sides applying a rule only one of them states is how a plugin claims
// `casting-time` and renders nothing on a page whose frontmatter says
// `casting_time` — with no error anywhere, because a key that does not match is a
// key the page does not have.
//
// **Folding is forgiving and that is ADR 0013's "read is forgiving; write is
// conventional".** The claim was written by a plugin author against a slug, and the
// key was written by a DM in whatever their editor's plugin produced. Neither is
// wrong and the application is a guest in both files.
//
// A page with two keys that fold to the same name — `casting_time` and
// `castingTime` — is matched by the *first in the file*, and the value is the first
// one's too. That is a YAML duplicate a DM's editor would have caught, and the
// answer is defined rather than arbitrary.
func NormaliseFieldKey(key string) string {
	folded := strings.Map(func(r rune) rune {
		switch r {
		case '_', '.', ' ', '\t':
			return '-'
		default:
			return r
		}
	}, strings.ToLower(key))

	return strings.Trim(folded, "-")
}
