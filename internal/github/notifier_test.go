package github

import (
	"testing"
)

func TestExtractCheckedDecisions(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no checkboxes",
			body: "Just some text\nwith no checkboxes",
			want: "",
		},
		{
			name: "unchecked checkboxes only",
			body: "- [ ] Option A\n- [ ] Option B",
			want: "",
		},
		{
			name: "one checked decision",
			body: "### Decision: DB\n- [x] PostgreSQL -- mature\n- [ ] SQLite -- simple",
			want: "- [x] PostgreSQL -- mature",
		},
		{
			name: "multiple checked decisions",
			body: "### Decision: DB\n- [x] PostgreSQL\n- [ ] SQLite\n### Decision: Cache\n- [ ] Redis\n- [x] Memcached",
			want: "- [x] PostgreSQL\n- [x] Memcached",
		},
		{
			name: "excludes Run tests checkbox",
			body: "- [x] Run tests before creating PR\n- [x] PostgreSQL",
			want: "- [x] PostgreSQL",
		},
		{
			name: "handles indented checkboxes",
			body: "  - [x] PostgreSQL\n  - [ ] SQLite",
			want: "- [x] PostgreSQL",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCheckedDecisions(tt.body)
			if got != tt.want {
				t.Errorf("extractCheckedDecisions() = %q, want %q", got, tt.want)
			}
		})
	}
}
