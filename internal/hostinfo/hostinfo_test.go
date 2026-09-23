package hostinfo

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestDetectRaspberryPi(t *testing.T) {
	root := fstest.MapFS{
		"etc/os-release": {Data: []byte(`PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
VERSION_CODENAME=bookworm
ID=debian
`)},
		"proc/meminfo":           {Data: []byte("MemTotal:        8245632 kB\nMemFree:  100 kB\n")},
		"proc/device-tree/model": {Data: []byte("Raspberry Pi 5 Model B Rev 1.0\x00")},
		"proc/self/mountinfo": {Data: []byte(
			"22 1 179:2 / / rw,noatime shared:1 - ext4 /dev/mmcblk0p2 rw\n" +
				"23 22 179:1 / /boot/firmware rw - vfat /dev/mmcblk0p1 rw\n")},
	}
	usage := func(string) (uint64, uint64, error) { return 50 << 30, 64 << 30, nil }

	got := detect(root, usage)
	if got.OSID != "debian" || got.OSVersionID != "12" || got.OSCodename != "bookworm" {
		t.Errorf("os = %q %q %q", got.OSID, got.OSVersionID, got.OSCodename)
	}
	if got.OSName != "Debian GNU/Linux 12 (bookworm)" {
		t.Errorf("OSName = %q", got.OSName)
	}
	if got.MemBytes != 8245632*1024 {
		t.Errorf("MemBytes = %d", got.MemBytes)
	}
	if !got.IsRaspberryPi() || got.Model != "Raspberry Pi 5 Model B Rev 1.0" {
		t.Errorf("Model = %q", got.Model)
	}
	if got.RootDevice != "/dev/mmcblk0p2" || !got.RootOnSDCard() {
		t.Errorf("RootDevice = %q", got.RootDevice)
	}
	if got.DiskFree != 50<<30 || got.DiskTotal != 64<<30 {
		t.Errorf("disk = %d/%d", got.DiskFree, got.DiskTotal)
	}
}

func TestDetectUbuntuUsesUbuntuCodename(t *testing.T) {
	root := fstest.MapFS{
		"etc/os-release": {Data: []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\nUBUNTU_CODENAME=noble\n")},
		"proc/self/mountinfo": {Data: []byte(
			"25 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw\n")},
	}
	usage := func(string) (uint64, uint64, error) { return 0, 0, errors.New("no statfs") }

	got := detect(root, usage)
	if got.OSID != "ubuntu" || got.OSVersionID != "24.04" || got.OSCodename != "noble" {
		t.Errorf("os = %q %q %q", got.OSID, got.OSVersionID, got.OSCodename)
	}
	if got.IsRaspberryPi() || got.RootOnSDCard() {
		t.Errorf("unexpected Pi/SD detection: %+v", got)
	}
	if got.MemBytes != 0 || got.DiskFree != 0 {
		t.Errorf("missing sources should leave zero values: %+v", got)
	}
}
