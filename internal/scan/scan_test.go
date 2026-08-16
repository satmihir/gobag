package scan

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satmihir/gobag/internal/testutil"
)

// awsKey is the fake key planted by the fixture workspace. It is a documented
// AWS example value, not a credential.
const awsKey = "AKIAIOSFODNN7EXAMPLE"

func TestReader(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		want []Finding
	}{
		{
			name: "aws access key",
			path: "notes/deploy.txt",
			body: "region = us-west-2\nAWS_ACCESS_KEY_ID=" + awsKey + "\n",
			want: []Finding{{Path: "notes/deploy.txt", Line: 2, Pattern: "AWS access key", Excerpt: "AKIA…"}},
		},
		{
			name: "private key block",
			path: "keys/backup",
			body: "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEA\n",
			want: []Finding{{Path: "keys/backup", Line: 1, Pattern: "private key block", Excerpt: "----…"}},
		},
		{
			name: "github classic token",
			path: "context/HANDOFF.md",
			body: "use ghp_" + strings.Repeat("a", 36) + " to push\n",
			want: []Finding{{Path: "context/HANDOFF.md", Line: 1, Pattern: "GitHub token", Excerpt: "ghp_…"}},
		},
		{
			name: "github fine-grained token",
			path: "context/HANDOFF.md",
			body: "github_pat_" + strings.Repeat("B", 30) + "\n",
			want: []Finding{{Path: "context/HANDOFF.md", Line: 1, Pattern: "GitHub token", Excerpt: "gith…"}},
		},
		{
			name: "slack token",
			path: "state/mcp.json",
			body: "{\n  \"slack\": \"xoxb-123456789012-abcdefghij\"\n}\n",
			want: []Finding{{Path: "state/mcp.json", Line: 2, Pattern: "Slack token", Excerpt: "xoxb…"}},
		},
		{
			name: "jwt",
			path: "state/session.json",
			body: "bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abc\n",
			want: []Finding{{Path: "state/session.json", Line: 1, Pattern: "JWT", Excerpt: "eyJh…"}},
		},
		{
			name: "generic assignment keeps the name and redacts the value",
			path: "state/memory/aws.md",
			body: "notes\napi_key: abcdefghijklmnop1234\n",
			want: []Finding{{Path: "state/memory/aws.md", Line: 2, Pattern: "generic secret assignment", Excerpt: "api_key: abcd…"}},
		},
		{
			name: "generic assignment matches password and token spellings",
			path: "config.yaml",
			body: "password = \"hunter2hunter2hunter2\"\nTOKEN:zzzzzzzzzzzzzzzzzzzz\n",
			want: []Finding{
				{Path: "config.yaml", Line: 1, Pattern: "generic secret assignment", Excerpt: "password = \"hunt…"},
				{Path: "config.yaml", Line: 2, Pattern: "generic secret assignment", Excerpt: "TOKEN:zzzz…"},
			},
		},
		{
			name: "one finding per pattern per line",
			path: "dump.txt",
			body: awsKey + " and " + awsKey + "\n",
			want: []Finding{{Path: "dump.txt", Line: 1, Pattern: "AWS access key", Excerpt: "AKIA…"}},
		},
		{
			name: "several patterns on one line are all reported",
			path: "dump.txt",
			body: "aws=" + awsKey + " secret=" + strings.Repeat("c", 20) + "\n",
			want: []Finding{
				{Path: "dump.txt", Line: 1, Pattern: "AWS access key", Excerpt: "AKIA…"},
				{Path: "dump.txt", Line: 1, Pattern: "generic secret assignment", Excerpt: "secret=cccc…"},
			},
		},
		{
			name: "suspicious filename with no content match",
			path: "infra/kubeconfig",
			body: "clusters: []\n",
			want: []Finding{{Path: "infra/kubeconfig", Pattern: PatternSuspiciousName}},
		},
		{
			name: "suspicious suffix",
			path: "certs/server.pem",
			body: "not actually a key\n",
			want: []Finding{{Path: "certs/server.pem", Pattern: PatternSuspiciousName}},
		},
		{
			name: "dotenv variant",
			path: ".env.production",
			body: "DEBUG=false\n",
			want: []Finding{{Path: ".env.production", Pattern: PatternSuspiciousName}},
		},
		{
			name: "filename warning precedes content matches",
			path: "notes/deploy.env",
			body: "AWS_ACCESS_KEY_ID=" + awsKey + "\n",
			want: []Finding{
				{Path: "notes/deploy.env", Pattern: PatternSuspiciousName},
				{Path: "notes/deploy.env", Line: 1, Pattern: "AWS access key", Excerpt: "AKIA…"},
			},
		},
		{
			name: "clean file",
			path: "context/HANDOFF.md",
			body: "# Handoff\n\nThe migration is half done. Tokens live in 1Password.\nsecret = short\napi_key: \n",
			want: nil,
		},
		{
			name: "empty file",
			path: "context/empty.md",
			body: "",
			want: nil,
		},
		{
			name: "no trailing newline",
			path: "dump.txt",
			body: "key " + awsKey,
			want: []Finding{{Path: "dump.txt", Line: 1, Pattern: "AWS access key", Excerpt: "AKIA…"}},
		},
		{
			name: "binary file is skipped",
			path: "build/gobag",
			body: "AWS_ACCESS_KEY_ID=" + awsKey + "\n\x00\x01\x02binary junk\n",
			want: nil,
		},
		{
			name: "binary file still gets its filename warning",
			path: "keys/id_rsa",
			body: "\x00\x00\x00\x00der encoded\n",
			want: []Finding{{Path: "keys/id_rsa", Pattern: PatternSuspiciousName}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Reader(tt.path, strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("Reader(%q): %v", tt.path, err)
			}
			assertFindings(t, got, tt.want)
			assertRedacted(t, got)
		})
	}
}

func TestReaderLongLineDoesNotError(t *testing.T) {
	// A line far longer than the read buffer must be scanned in chunks, not
	// rejected and not loaded whole.
	var b strings.Builder
	b.WriteString(strings.Repeat("a", 8*readChunk))
	b.WriteString(" " + awsKey + "\n")
	b.WriteString("xoxb-123456789012-abcdefghij\n")

	got, err := Reader("dump.txt", strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	assertFindings(t, got, []Finding{
		{Path: "dump.txt", Line: 1, Pattern: "AWS access key", Excerpt: "AKIA…"},
		{Path: "dump.txt", Line: 2, Pattern: "Slack token", Excerpt: "xoxb…"},
	})
}

func TestReaderOversizedFileIsNoted(t *testing.T) {
	r := io.MultiReader(
		strings.NewReader("AWS_ACCESS_KEY_ID="+awsKey+"\n"),
		io.LimitReader(&filler{}, MaxFileSize+1<<20),
	)
	got, err := Reader("dumps/heap.txt", r)
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	assertFindings(t, got, []Finding{
		{Path: "dumps/heap.txt", Line: 1, Pattern: "AWS access key", Excerpt: "AKIA…"},
		{Path: "dumps/heap.txt", Pattern: PatternUnscanned, Excerpt: reasonTooLarge},
	})
}

func TestFileFixtureWorkspace(t *testing.T) {
	if testing.Short() {
		t.Skip("fixture workspace shells out to git")
	}
	ws := testutil.NewWorkspace(t)

	planted := filepath.Join(ws.Root, "notes", "deploy.env")
	got, err := File(planted)
	if err != nil {
		t.Fatalf("File(%q): %v", planted, err)
	}
	assertFindings(t, got, []Finding{
		{Path: planted, Pattern: PatternSuspiciousName},
		{Path: planted, Line: 1, Pattern: "AWS access key", Excerpt: "AKIA…"},
	})
	assertRedacted(t, got)

	clean := filepath.Join(ws.Root, "context.md")
	got, err = File(clean)
	if err != nil {
		t.Fatalf("File(%q): %v", clean, err)
	}
	assertFindings(t, got, nil)
}

func TestFileMissing(t *testing.T) {
	if _, err := File(filepath.Join(t.TempDir(), "nope.md")); err == nil {
		t.Fatal("File on a missing path: want error, got nil")
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		name     string
		findings []Finding
		want     string
	}{
		{
			name: "nothing to report",
			want: "",
		},
		{
			name: "one finding",
			findings: []Finding{
				{Path: "notes/deploy.env", Line: 1, Pattern: "AWS access key", Excerpt: "AKIA…"},
			},
			want: "1 possible secret in a file about to be packed:\n" +
				"  notes/deploy.env:1  AWS access key  AKIA…\n" +
				"These are warnings only — gobag is packing them anyway.\n",
		},
		{
			name: "several findings",
			findings: []Finding{
				{Path: "notes/deploy.env", Line: 1, Pattern: "AWS access key", Excerpt: "AKIA…"},
				{Path: "state/memory/aws.md", Line: 7, Pattern: "generic secret assignment", Excerpt: "api_key: abcd…"},
			},
			want: "2 possible secrets in files about to be packed:\n" +
				"  notes/deploy.env:1  AWS access key  AKIA…\n" +
				"  state/memory/aws.md:7  generic secret assignment  api_key: abcd…\n" +
				"These are warnings only — gobag is packing them anyway.\n",
		},
		{
			name: "path-based finding has no line or excerpt",
			findings: []Finding{
				{Path: "infra/kubeconfig", Pattern: PatternSuspiciousName},
			},
			want: "1 possible secret in a file about to be packed:\n" +
				"  infra/kubeconfig  suspicious filename\n" +
				"These are warnings only — gobag is packing them anyway.\n",
		},
		{
			name: "skips are listed separately and not counted",
			findings: []Finding{
				{Path: "notes/deploy.env", Line: 1, Pattern: "AWS access key", Excerpt: "AKIA…"},
				{Path: "dumps/heap.txt", Pattern: PatternUnscanned, Excerpt: reasonTooLarge},
			},
			want: "1 possible secret in a file about to be packed:\n" +
				"  notes/deploy.env:1  AWS access key  AKIA…\n" +
				"Not scanned (over 10MB): dumps/heap.txt\n" +
				"These are warnings only — gobag is packing them anyway.\n",
		},
		{
			name: "only a skip",
			findings: []Finding{
				{Path: "dumps/heap.txt", Pattern: PatternUnscanned, Excerpt: reasonTooLarge},
			},
			want: "Not scanned (over 10MB): dumps/heap.txt\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			if err := Format(tt.findings, &b); err != nil {
				t.Fatalf("Format: %v", err)
			}
			if b.String() != tt.want {
				t.Errorf("Format output:\ngot:\n%s\nwant:\n%s", b.String(), tt.want)
			}
		})
	}
}

func assertFindings(t *testing.T, got, want []Finding) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d findings, want %d:\ngot:  %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding %d:\ngot:  %+v\nwant: %+v", i, got[i], want[i])
		}
	}
}

// assertRedacted guards the promise that the report never leaks a whole
// credential.
func assertRedacted(t *testing.T, findings []Finding) {
	t.Helper()
	for _, f := range findings {
		if strings.Contains(f.Excerpt, awsKey) {
			t.Errorf("excerpt %q leaks the full key", f.Excerpt)
		}
		if f.Line > 0 && f.Excerpt != "" && !strings.HasSuffix(f.Excerpt, "…") {
			t.Errorf("excerpt %q is not redacted", f.Excerpt)
		}
	}
}

// filler streams filler text without allocating it, for oversized-input tests.
type filler struct{ n int64 }

func (f *filler) Read(p []byte) (int, error) {
	for i := range p {
		if f.n%80 == 79 {
			p[i] = '\n'
		} else {
			p[i] = 'a'
		}
		f.n++
	}
	return len(p), nil
}
