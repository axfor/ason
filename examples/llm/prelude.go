package llm

// Prelude holds the facts a protocol collects while scanning that the integration layer needs for its side effects
// (request headers, context keys). "Seen" and "value" are kept apart: not seen is not the same as false.
type Prelude struct {
	Model      string
	ModelSeen  bool
	Stream     bool
	StreamSeen bool
}

// Preluder is implemented by protocols that report a Prelude to the integration layer.
type Preluder interface {
	Prelude() Prelude
}
