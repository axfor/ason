package llm

// 协议热路径上的常量字节串：包级变量，避免每次回调都分配。
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
