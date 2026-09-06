package llm

// Constant byte strings on the protocol hot paths: package-level variables so no callback allocates them.
var (
	lit0  = []byte(`"`)
	lit1  = []byte("data:")
	lit2  = []byte(`{"type":"url","url":"`)
	lit3  = []byte(`"}`)
	lit4  = []byte("base64,")
	lit5  = []byte(`[{"text":"`)
	lit6  = []byte(`"}]`)
	lit7  = []byte("[]")
	lit8  = []byte("http://")
	lit9  = []byte("https://")
	lit10 = []byte(`{"type":"enabled"}`)
	lit11 = []byte("false")
)
