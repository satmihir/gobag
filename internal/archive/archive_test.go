package archive

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// age's default scrypt work factor costs ~1s per operation by design.
	// Tests exercise the pipeline, not the KDF.
	ScryptWorkFactor = 10
	os.Exit(m.Run())
}

type entry struct {
	path string
	body string
}

var corpus = []entry{
	{"MANIFEST.json", `{"name":"teammate"}`},
	{"context/HANDOFF.md", "# Handoff\n\nMid-flight: the schema migration.\n"},
	{"state/memory/conventions.md", "Squash merges only.\n"},
}

func build(t *testing.T, passphrase string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, passphrase)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, e := range corpus {
		if err := w.AddBytes(e.path, 0o644, []byte(e.body)); err != nil {
			t.Fatalf("AddBytes(%s): %v", e.path, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

func TestRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name       string
		passphrase string
	}{
		{"plaintext", ""},
		{"encrypted", "correct horse battery staple"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := build(t, tc.passphrase)

			// An encrypted archive must not leak payload bytes.
			if tc.passphrase != "" {
				if !bytes.HasPrefix(raw, []byte(ageHeader)) {
					t.Error("encrypted archive does not start with the age header")
				}
				if bytes.Contains(raw, []byte("schema migration")) {
					t.Error("plaintext content found in encrypted archive")
				}
			}

			got := map[string]string{}
			r, err := NewReader(bytes.NewReader(raw), tc.passphrase)
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			defer r.Close()
			for {
				hdr, err := r.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("Next: %v", err)
				}
				var b bytes.Buffer
				if _, err := b.ReadFrom(r); err != nil {
					t.Fatalf("read %s: %v", hdr.Name, err)
				}
				got[hdr.Name] = b.String()
			}

			for _, e := range corpus {
				if got[e.path] != e.body {
					t.Errorf("%s: content mismatch\n got %q\nwant %q", e.path, got[e.path], e.body)
				}
			}
			if _, ok := got[ChecksumsName]; !ok {
				t.Error("archive has no CHECKSUMS entry")
			}
		})
	}
}

func TestVerifyAcceptsIntactArchive(t *testing.T) {
	for _, tc := range []struct {
		name       string
		passphrase string
	}{
		{"plaintext", ""},
		{"encrypted", "hunter2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results, err := Verify(bytes.NewReader(build(t, tc.passphrase)), tc.passphrase)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if len(results) != len(corpus) {
				t.Fatalf("got %d results, want %d", len(results), len(corpus))
			}
			for _, r := range results {
				if !r.OK {
					t.Errorf("%s: not OK (%s)", r.Path, r.Note)
				}
			}
		})
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	for _, tc := range []struct {
		name       string
		passphrase string
	}{
		{"plaintext", ""},
		{"encrypted", "hunter2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := build(t, tc.passphrase)
			// Flip a bit past the header, inside the payload.
			raw[len(raw)-16] ^= 0x40

			results, err := Verify(bytes.NewReader(raw), tc.passphrase)
			if err != nil {
				return // detected at the decrypt/decompress layer, which counts
			}
			for _, r := range results {
				if !r.OK {
					return // detected by checksum comparison
				}
			}
			t.Error("corruption went undetected")
		})
	}
}

func TestPassphraseErrors(t *testing.T) {
	encrypted := build(t, "right")
	plaintext := build(t, "")

	for _, tc := range []struct {
		name    string
		raw     []byte
		pass    string
		wantErr error
	}{
		{"wrong passphrase", encrypted, "wrong", ErrWrongPassphrase},
		{"missing passphrase", encrypted, "", ErrPassphraseRequired},
		{"needless passphrase", plaintext, "unnecessary", ErrNotEncrypted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewReader(bytes.NewReader(tc.raw), tc.pass)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestNotAnArchive(t *testing.T) {
	if _, err := NewReader(strings.NewReader("no"), ""); !errors.Is(err, ErrNotAnArchive) {
		t.Fatalf("got %v, want ErrNotAnArchive", err)
	}
}

func TestWriterRejectsBadEntries(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, "")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	if err := w.AddBytes(ChecksumsName, 0o644, []byte("x")); err == nil {
		t.Error("writing a reserved CHECKSUMS entry should fail")
	}
	if err := w.AddBytes("a.md", 0o644, []byte("x")); err != nil {
		t.Fatalf("AddBytes: %v", err)
	}
	if err := w.AddBytes("a.md", 0o644, []byte("y")); err == nil {
		t.Error("duplicate entry should fail")
	}
}

func TestInspectReadsManifestFirst(t *testing.T) {
	c, err := Inspect(bytes.NewReader(build(t, "")), "")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if c.Manifest == nil || c.Manifest.Name != "teammate" {
		t.Fatalf("manifest not decoded: %+v", c.Manifest)
	}
	if len(c.Entries) != len(corpus)+1 { // +1 for CHECKSUMS
		t.Errorf("got %d entries, want %d", len(c.Entries), len(corpus)+1)
	}
}

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		name              string
		recorded          map[string]string
		computed          map[string]string
		wantOK, wantNotOK int
	}{
		{
			name:     "all match",
			recorded: map[string]string{"a": "1", "b": "2"},
			computed: map[string]string{"a": "1", "b": "2"},
			wantOK:   2,
		},
		{
			name:      "digest mismatch",
			recorded:  map[string]string{"a": "1"},
			computed:  map[string]string{"a": "9"},
			wantNotOK: 1,
		},
		{
			name:      "entry missing from archive",
			recorded:  map[string]string{"a": "1", "gone": "2"},
			computed:  map[string]string{"a": "1"},
			wantOK:    1,
			wantNotOK: 1,
		},
		{
			name:      "entry absent from checksums",
			recorded:  map[string]string{"a": "1"},
			computed:  map[string]string{"a": "1", "extra": "3"},
			wantOK:    1,
			wantNotOK: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ok, notOK int
			for _, r := range compare(tc.recorded, tc.computed) {
				if r.OK {
					ok++
				} else {
					notOK++
				}
			}
			if ok != tc.wantOK || notOK != tc.wantNotOK {
				t.Errorf("got %d ok / %d not ok, want %d / %d", ok, notOK, tc.wantOK, tc.wantNotOK)
			}
		})
	}
}
