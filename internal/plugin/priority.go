package plugin

// Priority orders a plugin's capabilities against every other plugin's.
//
// It is a small signed integer rather than an enum because the interesting values
// are the ones between the named ones, and two house-rule plugins that both want to
// run "after the core" should not have to coordinate to get a number between them.
// The three constants are the three positions everybody actually uses.
type Priority int

const (
	// PriorityFirst runs before everything at the default priority. It is for a
	// plugin that *produces* something the others consume — a goldmark extension
	// that defines the node another plugin's hook looks for.
	PriorityFirst Priority = -100

	// PriorityNormal is the default: a plugin that neither feeds nor is fed on.
	PriorityNormal Priority = 0

	// PriorityLast runs after everything at the default priority, and is for a
	// hook that post-processes output rather than contributing to it.
	PriorityLast Priority = 100
)

// Prioritised is the optional interface a plugin implements to order itself.
//
// It is optional rather than a field of [Plugin] because the common case is that a
// plugin does not care, and an interface with one method that most plugins do not
// implement says "most plugins do not care" in the type system rather than in a
// default value. `Plugin` itself stays three methods, so adding an ordering knob to
// it would be a change to every plugin that exists.
type Prioritised interface {
	// Priority is where this plugin's capabilities sit. It is read once, in
	// [Registry.Add]; changing it afterwards has no effect, because the order is
	// sorted then and the sorted slice is what everything else reads.
	Priority() Priority
}
