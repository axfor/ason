package ason

import "testing"

type sharedTreeReq struct {
	Model    string            `json:"model"`
	Stream   bool              `json:"stream"`
	Messages []map[string]any  `json:"messages"`
	Metadata map[string]string `json:"metadata"`
}

// A tree from FieldTreeOf carries the root fields' types ready: setting it on a transformer allocates nothing,
// where it used to build the same map for every transformer.
func TestSetFieldTreeSharesTheRootTypes(t *testing.T) {
	tree := FieldTreeOf(&sharedTreeReq{}, 3)
	tr := NewTransformer(renameModelProto{})
	if n := testing.AllocsPerRun(100, func() { tr.fieldTypes = nil; tr.SetFieldTree(tree) }); n != 0 {
		t.Fatalf("SetFieldTree allocated %v times per call", n)
	}
	if tr.fieldTypes["stream"] != tree.Keys["stream"].Types || len(tr.fieldTypes) != len(tree.Keys) {
		t.Fatalf("the shared table does not match the tree: %v", tr.fieldTypes)
	}
	// A tree put together by hand has no table ready and still gets one.
	hand := &FieldTree{Keys: map[string]*FieldTree{"model": {Types: TypeString}}}
	tr2 := NewTransformer(renameModelProto{})
	tr2.SetFieldTree(hand)
	if tr2.fieldTypes["model"] != TypeString {
		t.Fatalf("a hand-built tree lost its root types: %v", tr2.fieldTypes)
	}
}
