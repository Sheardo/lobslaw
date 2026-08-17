package node

import (
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/config"
)

// A [[storage.mounts]] entry has to arrive at the backend intact.
//
// It did not. seedStorageMounts copied label, type, path and bucket
// and dropped everything else, and the rclone backend refuses a mount
// whose remote is empty — so a `type = "rclone"` mount declared in
// config could never start. There was no `remote` key to set either,
// which is why nobody had noticed: the section could not express a
// working remote mount at all.

func TestTheRemoteReachesTheBackend(t *testing.T) {
	t.Parallel()
	got := mountToProto(config.StorageMountConfig{
		Label: "archive", Type: "rclone", Remote: "r2", Bucket: "backups",
	})
	if got.GetRemote() != "r2" {
		t.Errorf("remote = %q; an rclone mount with no remote cannot start", got.GetRemote())
	}
	if got.GetBucket() != "backups" || got.GetLabel() != "archive" || got.GetType() != "rclone" {
		t.Errorf("mount = %+v", got)
	}
}

// Options carry the endpoint and the credential references. The
// backend splits "_ref" keys out and resolves them as secrets, so
// dropping the map costs both the endpoint and the ability to
// authenticate.
func TestOptionsReachTheBackend(t *testing.T) {
	t.Parallel()
	got := mountToProto(config.StorageMountConfig{
		Label: "archive", Type: "rclone", Remote: "r2",
		Options: map[string]string{
			"endpoint":       "https://example.r2.cloudflarestorage.com",
			"access_key_ref": "env:R2_ACCESS_KEY",
			"secret_key_ref": "env:R2_SECRET_KEY",
		},
	})
	for key, want := range map[string]string{
		"endpoint":       "https://example.r2.cloudflarestorage.com",
		"access_key_ref": "env:R2_ACCESS_KEY",
		"secret_key_ref": "env:R2_SECRET_KEY",
	} {
		if got.GetOptions()[key] != want {
			t.Errorf("options[%q] = %q, want %q", key, got.GetOptions()[key], want)
		}
	}
}

// The config is hot-reloadable and the proto is about to be
// replicated. Sharing the map's backing store would let a later reload
// mutate a payload already handed to raft.
func TestOptionsAreClonedNotAliased(t *testing.T) {
	t.Parallel()
	src := config.StorageMountConfig{
		Label: "archive", Type: "rclone", Remote: "r2",
		Options: map[string]string{"endpoint": "https://one.example"},
	}
	got := mountToProto(src)
	src.Options["endpoint"] = "https://two.example"
	if got.GetOptions()["endpoint"] != "https://one.example" {
		t.Errorf("mutating the config changed an already-built mount: %q",
			got.GetOptions()["endpoint"])
	}
}

// A local mount declares no options, and a nil map must stay nil
// rather than becoming an empty one — the two are the same to a
// reader, but only one of them round-trips through proto equality.
func TestALocalMountNeedsNoOptions(t *testing.T) {
	t.Parallel()
	got := mountToProto(config.StorageMountConfig{
		Label: "work", Type: "local", Path: "/srv/work",
	})
	if len(got.GetOptions()) != 0 {
		t.Errorf("options = %v, want none", got.GetOptions())
	}
	if got.GetPath() != "/srv/work" {
		t.Errorf("path = %q", got.GetPath())
	}
}

// The NFS backend refuses a mount with no server or export, and there
// was no key for either — so `type = "nfs"` was as unreachable from
// config as rclone was.
func TestTheNFSServerAndExportReachTheBackend(t *testing.T) {
	t.Parallel()
	got := mountToProto(config.StorageMountConfig{
		Label: "nas", Type: "nfs", Server: "nas.local", Export: "/volume1/lobslaw",
	})
	if got.GetServer() != "nas.local" {
		t.Errorf("server = %q; an NFS mount without one cannot start", got.GetServer())
	}
	if got.GetExport() != "/volume1/lobslaw" {
		t.Errorf("export = %q; an NFS mount without one cannot start", got.GetExport())
	}
}
