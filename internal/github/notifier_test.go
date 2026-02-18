package github

import (
	"strings"
	"testing"

	"github.com/jcwearn/agent-operator/internal/controller"
)

func TestSplitComment(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		maxLen     int
		wantChunks int
		check      func(t *testing.T, chunks []string)
	}{
		{
			name:       "short body returns single chunk",
			body:       "Hello world",
			maxLen:     100,
			wantChunks: 1,
		},
		{
			name:       "exactly at limit returns single chunk",
			body:       strings.Repeat("a", 100),
			maxLen:     100,
			wantChunks: 1,
		},
		{
			name:       "splits at paragraph break",
			body:       "First paragraph\n\nSecond paragraph\n\nThird paragraph",
			maxLen:     40,
			wantChunks: 2,
			check: func(t *testing.T, chunks []string) {
				// "First paragraph\n\nSecond paragraph" (34 chars) fits in 40-char limit.
				if chunks[0] != "First paragraph\n\nSecond paragraph" {
					t.Errorf("chunk 0 = %q, want %q", chunks[0], "First paragraph\\n\\nSecond paragraph")
				}
				if chunks[1] != "Third paragraph" {
					t.Errorf("chunk 1 = %q, want %q", chunks[1], "Third paragraph")
				}
			},
		},
		{
			name:       "splits at section header",
			body:       "## Section 1\n\nContent one.\n\n## Section 2\n\nContent two.",
			maxLen:     35,
			wantChunks: 2,
			check: func(t *testing.T, chunks []string) {
				if !strings.HasPrefix(chunks[0], "## Section 1") {
					t.Errorf("chunk 0 should start with section 1 header, got %q", chunks[0])
				}
			},
		},
		{
			name:       "all chunks within limit",
			body:       strings.Repeat("word ", 200),
			maxLen:     100,
			wantChunks: 0, // just check all fit
			check: func(t *testing.T, chunks []string) {
				for i, c := range chunks {
					if len(c) > 100 {
						t.Errorf("chunk %d length %d exceeds maxLen 100", i, len(c))
					}
				}
				// Verify all content is preserved.
				joined := strings.Join(chunks, "\n")
				origWords := strings.Fields(strings.Repeat("word ", 200))
				joinedWords := strings.Fields(joined)
				if len(origWords) != len(joinedWords) {
					t.Errorf("word count: got %d, want %d", len(joinedWords), len(origWords))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitComment(tt.body, tt.maxLen)
			if tt.wantChunks > 0 && len(chunks) != tt.wantChunks {
				t.Errorf("splitComment() returned %d chunks, want %d", len(chunks), tt.wantChunks)
			}
			if tt.check != nil {
				tt.check(t, chunks)
			}
		})
	}
}

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

func TestParseModelSelections(t *testing.T) {
	tests := []struct {
		name string
		body string
		want controller.ModelSelectionResult
	}{
		{
			name: "all defaults unchanged",
			body: `## Model Selection

Select the Claude model for each workflow step, then react with :+1: to confirm.

### Plan
- [x] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Implement
- [x] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Test
- [x] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Pull Request
- [x] Haiku 4.5 — fastest, lower cost
- [ ] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower

---
**To confirm**, react with :+1: on this comment.`,
			want: controller.ModelSelectionResult{
				Plan:      "sonnet",
				Implement: "sonnet",
				Test:      "sonnet",
				PR:        "haiku",
			},
		},
		{
			name: "all changed to opus",
			body: `## Model Selection

### Plan
- [ ] Sonnet 4.5 — balanced speed and capability
- [x] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Implement
- [ ] Sonnet 4.5 — balanced speed and capability
- [x] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Test
- [ ] Sonnet 4.5 — balanced speed and capability
- [x] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Pull Request
- [ ] Haiku 4.5 — fastest, lower cost
- [ ] Sonnet 4.5 — balanced speed and capability
- [x] Opus 4 — most capable, slower`,
			want: controller.ModelSelectionResult{
				Plan:      "opus",
				Implement: "opus",
				Test:      "opus",
				PR:        "opus",
			},
		},
		{
			name: "mixed selections",
			body: `## Model Selection

### Plan
- [ ] Sonnet 4.5 — balanced speed and capability
- [x] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Implement
- [x] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Test
- [ ] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower
- [x] Haiku 4.5 — fastest, lower cost

### Pull Request
- [x] Haiku 4.5 — fastest, lower cost
- [ ] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower`,
			want: controller.ModelSelectionResult{
				Plan:      "opus",
				Implement: "sonnet",
				Test:      "haiku",
				PR:        "haiku",
			},
		},
		{
			name: "multiple checked in section uses first",
			body: `## Model Selection

### Plan
- [x] Sonnet 4.5 — balanced speed and capability
- [x] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Implement
- [x] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Test
- [x] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Pull Request
- [x] Haiku 4.5 — fastest, lower cost
- [ ] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower`,
			want: controller.ModelSelectionResult{
				Plan:      "sonnet",
				Implement: "sonnet",
				Test:      "sonnet",
				PR:        "haiku",
			},
		},
		{
			name: "no checked in section uses fallback",
			body: `## Model Selection

### Plan
- [ ] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Implement
- [x] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Test
- [x] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower
- [ ] Haiku 4.5 — fastest, lower cost

### Pull Request
- [ ] Haiku 4.5 — fastest, lower cost
- [ ] Sonnet 4.5 — balanced speed and capability
- [ ] Opus 4 — most capable, slower`,
			want: controller.ModelSelectionResult{
				Plan:      "sonnet",
				Implement: "sonnet",
				Test:      "sonnet",
				PR:        "haiku",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &Notifier{}
			got := n.parseModelSelections(tt.body)
			if got.Plan != tt.want.Plan {
				t.Errorf("Plan = %q, want %q", got.Plan, tt.want.Plan)
			}
			if got.Implement != tt.want.Implement {
				t.Errorf("Implement = %q, want %q", got.Implement, tt.want.Implement)
			}
			if got.Test != tt.want.Test {
				t.Errorf("Test = %q, want %q", got.Test, tt.want.Test)
			}
			if got.PR != tt.want.PR {
				t.Errorf("PR = %q, want %q", got.PR, tt.want.PR)
			}
		})
	}
}
