package badger

// Config contains only backend durability knobs. The host supplies the
// filesystem path to Open; no Balda configuration or locator policy is
// imported by this adapter.
type Config struct {
	SyncWrites        bool
	DetectConflicts   bool
	NumVersionsToKeep int
}

// Store is the public name for the canonical Badger owner.
type Store = BadgerSessionMemoryStore

// DefaultConfig returns the canonical durability defaults used by Balda's
// existing store format.
func DefaultConfig() Config {
	return Config{SyncWrites: true, DetectConflicts: true, NumVersionsToKeep: 1}
}

// Open opens a canonical store at a host-supplied path. The variadic config
// keeps the one-argument form convenient while allowing future hosts to opt
// into backend settings without changing the package boundary.
func Open(directory string, configs ...Config) (*Store, error) {
	config := DefaultConfig()
	if len(configs) > 0 {
		config = configs[0]
	}
	return openWithConfig(directory, config)
}
