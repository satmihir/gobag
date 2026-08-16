package gitops

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/satmihir/gobag/internal/manifest"
	"github.com/satmihir/gobag/internal/testutil"
)

func TestRemoteReality(t *testing.T) {
	tests := []struct {
		name string
		// arrange runs after the repository has been installed at its pinned
		// ref, and may move the remote or break it.
		arrange         func(t *testing.T, ws *testutil.Workspace, s *manifest.Source)
		wantAhead       int
		wantUnreachable bool
		// wantMoved says whether RemoteHead should differ from the pinned ref.
		wantMoved bool
	}{
		{
			name:    "nothing happened while the bag was in transit",
			arrange: func(*testing.T, *testutil.Workspace, *manifest.Source) {},
		},
		{
			name: "main advanced fourteen commits",
			arrange: func(_ *testing.T, ws *testutil.Workspace, _ *manifest.Source) {
				ws.AdvanceRemote("frontend", 14)
			},
			wantAhead: 14,
			wantMoved: true,
		},
		{
			name: "a single commit",
			arrange: func(_ *testing.T, ws *testutil.Workspace, _ *manifest.Source) {
				ws.AdvanceRemote("frontend", 1)
			},
			wantAhead: 1,
			wantMoved: true,
		},
		{
			name: "the remote is gone",
			arrange: func(t *testing.T, ws *testutil.Workspace, _ *manifest.Source) {
				if err := os.RemoveAll(filepath.Join(ws.RemotesDir, "frontend.git")); err != nil {
					t.Fatal(err)
				}
			},
			wantUnreachable: true,
		},
		{
			name: "the manifest records no remote",
			arrange: func(_ *testing.T, _ *testutil.Workspace, s *manifest.Source) {
				s.Remote = ""
			},
			wantUnreachable: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			root := installRoot(t)

			s := installed(t, ws, "frontend", "main")
			mustEnsureRepo(t, root, s)
			pinned := s.Ref

			tc.arrange(t, ws, &s)

			got, err := RemoteReality(root, s)
			if err != nil {
				t.Fatalf("RemoteReality: %v", err)
			}
			if got.Dest != s.Dest {
				t.Errorf("dest = %q, want %q", got.Dest, s.Dest)
			}
			if got.PinnedRef != pinned {
				t.Errorf("pinned ref = %q, want %q", got.PinnedRef, pinned)
			}
			if got.Unreachable != tc.wantUnreachable {
				t.Fatalf("unreachable = %v, want %v (%+v)", got.Unreachable, tc.wantUnreachable, got)
			}
			if tc.wantUnreachable {
				return
			}
			if got.Ahead != tc.wantAhead {
				t.Errorf("ahead = %d, want %d", got.Ahead, tc.wantAhead)
			}
			if tc.wantMoved {
				if got.RemoteHead == pinned || got.RemoteHead == "" {
					t.Errorf("remote head = %q, want a commit past %q", got.RemoteHead, pinned)
				}
				if want := remoteHead(t, ws, "frontend", "main"); got.RemoteHead != want {
					t.Errorf("remote head = %q, want %q", got.RemoteHead, want)
				}
			} else if got.RemoteHead != pinned {
				t.Errorf("remote head = %q, want the pinned %q", got.RemoteHead, pinned)
			}
		})
	}
}

// TestRemoteRealityWithoutACheckout covers reconciliation running for a
// repository whose clone never landed: the remote can still be consulted,
// there is just nothing local to count against.
func TestRemoteRealityWithoutACheckout(t *testing.T) {
	ws := fixture(t)
	root := installRoot(t)

	s := installed(t, ws, "frontend", "main")
	ws.AdvanceRemote("frontend", 4)

	got, err := RemoteReality(root, s)
	if err != nil {
		t.Fatalf("RemoteReality: %v", err)
	}
	if got.Unreachable {
		t.Fatal("the remote is reachable, only the checkout is missing")
	}
	if want := remoteHead(t, ws, "frontend", "main"); got.RemoteHead != want {
		t.Errorf("remote head = %q, want %q", got.RemoteHead, want)
	}
	if got.Ahead != 0 {
		t.Errorf("ahead = %d, want 0 with no local objects to count against", got.Ahead)
	}
}

// TestRemoteRealityDetachedPin exercises the branch-less pin, which asks the
// remote for its HEAD instead of a named branch.
func TestRemoteRealityDetachedPin(t *testing.T) {
	ws := fixture(t)
	root := installRoot(t)

	s := installed(t, ws, "frontend", "")
	mustEnsureRepo(t, root, s)
	ws.AdvanceRemote("frontend", 2)

	got, err := RemoteReality(root, s)
	if err != nil {
		t.Fatalf("RemoteReality: %v", err)
	}
	if got.Unreachable {
		t.Fatal("remote reported unreachable")
	}
	if got.Ahead != 2 {
		t.Errorf("ahead = %d, want 2", got.Ahead)
	}
}

func TestRemoteRealityRejectsBadDest(t *testing.T) {
	ws := fixture(t)
	s := installed(t, ws, "frontend", "main")
	s.Dest = "../escape"
	if _, err := RemoteReality(installRoot(t), s); err == nil {
		t.Fatal("expected an error for an escaping destination")
	}
}
