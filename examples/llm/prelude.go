package llm

// Prelude 是协议在扫描过程中收集到的、集成层需要用来产生副作用的事实
// （请求头、上下文键）。字段"是否已见"与"值"分开：没见到不等于 false。
type Prelude struct {
	Model      string
	ModelSeen  bool
	Stream     bool
	StreamSeen bool
}

// Preluder 由需要向集成层报告 Prelude 的协议实现。
type Preluder interface {
	Prelude() Prelude
}
