package eval

import "testing"

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name  string
		set   Set
		found [][]string
		want  Metrics
	}{
		{
			name: "全部命中且第一",
			set: Set{Queries: []Query{
				{Query: "q1", Expected: []string{"a"}},
				{Query: "q2", Expected: []string{"b"}},
			}},
			found: [][]string{{"a", "x"}, {"b", "y"}},
			want:  Metrics{Queries: 2, Hits: 2, RecallAt5: 1, MRR: 1},
		},
		{
			name:  "命中但排名第二",
			set:   Set{Queries: []Query{{Query: "q", Expected: []string{"a"}}}},
			found: [][]string{{"x", "a"}},
			want:  Metrics{Queries: 1, Hits: 1, RecallAt5: 1, MRR: 0.5},
		},
		{
			name:  "多期望只命中一半",
			set:   Set{Queries: []Query{{Query: "q", Expected: []string{"a", "b"}}}},
			found: [][]string{{"a", "x"}},
			want:  Metrics{Queries: 1, Hits: 1, RecallAt5: 0.5, MRR: 1},
		},
		{
			name:  "无结果",
			set:   Set{Queries: []Query{{Query: "q", Expected: []string{"a"}}}},
			found: [][]string{{}},
			want:  Metrics{Queries: 1, NoResults: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.set.Evaluate(tt.found)
			if got != tt.want {
				t.Fatalf("Evaluate = %+v, want %+v", got, tt.want)
			}
		})
	}
}
