package ason

import (
	"strings"
	"testing"
)

// Via: every callback inside the subtree goes to the sub-hook and OnLeave of the container goes back to the issuer; deeper levels the sub-hook enters itself are its own.
type viaHost struct {
	BaseProtocol
	sub   viaSub
	trace *[]string
}
type viaSub struct {
	BaseProtocol
	trace *[]string
}

func (p *viaHost) OnKey(t *Transformer) Action {
	*p.trace = append(*p.trace, "host.key:"+t.PathString())
	if t.Depth() == 1 && t.Last() == "sub" {
		return Probe()
	}
	return Pass()
}
func (p *viaHost) OnStart(t *Transformer, kind ValueKind) Action {
	*p.trace = append(*p.trace, "host.start:"+t.PathString())
	return Enter().Via(&p.sub)
}
func (p *viaHost) OnLeave(t *Transformer) { *p.trace = append(*p.trace, "host.leave:"+t.PathString()) }
func (s *viaSub) OnKey(t *Transformer) Action {
	*s.trace = append(*s.trace, "sub.key:"+t.PathString())
	if t.Last() == "in" {
		return Probe()
	}
	return Pass()
}
func (s *viaSub) OnElem(t *Transformer) Action {
	*s.trace = append(*s.trace, "sub.elem:"+t.PathString())
	return Pass()
}
func (s *viaSub) OnStart(t *Transformer, kind ValueKind) Action {
	*s.trace = append(*s.trace, "sub.start:"+t.PathString())
	return Enter() // the sub-hook enters itself: deeper levels stay with it
}
func (s *viaSub) OnLeave(t *Transformer) { *s.trace = append(*s.trace, "sub.leave:"+t.PathString()) }

func TestVia(t *testing.T) {
	in := `{"a":1,"sub":{"x":[1,2],"in":{"y":2}},"b":3}`
	for _, cs := range []int{1, 3, 4096} {
		var trace []string
		h := &viaHost{trace: &trace}
		h.sub.trace = &trace
		out, ok, why := feedAll(NewTransformer(h), in, cs)
		if !ok {
			t.Fatalf("chunk=%d: %s", cs, why)
		}
		if out != in {
			t.Fatalf("chunk=%d: output %s", cs, out)
		}
		want := []string{
			"host.key:a", "host.key:sub", "host.start:sub",
			"sub.key:sub.x", "sub.key:sub.in", "sub.start:sub.in", "sub.key:sub.in.y", "sub.leave:sub.in",
			"host.leave:sub", "host.key:b", "host.leave:", // last: the root object closing
		}
		got := strings.Join(trace, " ")
		if got != strings.Join(want, " ") {
			t.Fatalf("chunk=%d wrong callback sequence:\n got  %s\n want %s", cs, got, strings.Join(want, " "))
		}
	}
}
