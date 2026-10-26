package workflow

import (
	"strings"
	"testing"
)

func TestParseDefinition(t *testing.T) {
	def, err := Parse([]byte(`
name: demo
nodes:
  - id: extract
    action: pdf_extract
    params:
      path: /tmp/a.pdf
    timeout: 5s
  - id: replace
    action: http_request
    depends_on: [extract]
    retries: 2
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if def.Name != "demo" || len(def.Nodes) != 2 {
		t.Fatalf("def = %+v", def)
	}
	order, err := def.TopoOrder()
	if err != nil || order[0].ID != "extract" || order[1].ID != "replace" {
		t.Fatalf("order = %+v, err=%v", order, err)
	}
}

func TestParseDefinitionErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "empty name", yaml: "nodes: [{id: a, action: noop}]", want: "name"},
		{name: "no nodes", yaml: "name: x", want: "nodes"},
		{name: "duplicate id", yaml: "name: x\nnodes: [{id: a, action: noop}, {id: a, action: noop}]", want: "重复"},
		{name: "missing dep", yaml: "name: x\nnodes: [{id: a, action: noop, depends_on: [b]}]", want: "依赖"},
		{name: "cycle", yaml: "name: x\nnodes: [{id: a, action: noop, depends_on: [b]}, {id: b, action: noop, depends_on: [a]}]", want: "环"},
		{name: "bad timeout", yaml: "name: x\nnodes: [{id: a, action: noop, timeout: nope}]", want: "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want contains %q", err, tt.want)
			}
		})
	}
}
