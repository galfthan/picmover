package gvfs

import (
	"path/filepath"
	"testing"
)

func TestParseMountAndURI(t *testing.T) {
	const dir = "/run/user/1000/gvfs"
	cases := []struct {
		p                 string
		scheme, host, rel string
		ok, device        bool
		uri               string
	}{
		{p: dir + "/mtp:host=Google_Pixel_7_1A2B3C4D/Internal shared storage/DCIM/Camera/PXL_2025 (1).jpg",
			scheme: "mtp", host: "Google_Pixel_7_1A2B3C4D", rel: "Internal shared storage/DCIM/Camera/PXL_2025 (1).jpg",
			ok: true, device: true,
			uri: "mtp://Google_Pixel_7_1A2B3C4D/Internal%20shared%20storage/DCIM/Camera/PXL_2025%20%281%29.jpg"},
		{p: dir + "/gphoto2:host=Apple_Inc._iPhone_00008110001A2B3C0E9A401E/DCIM/100APPLE/IMG_0001.HEIC",
			scheme: "gphoto2", host: "Apple_Inc._iPhone_00008110001A2B3C0E9A401E", rel: "DCIM/100APPLE/IMG_0001.HEIC",
			ok: true, device: true,
			uri: "gphoto2://Apple_Inc._iPhone_00008110001A2B3C0E9A401E/DCIM/100APPLE/IMG_0001.HEIC"},
		{p: dir + "/mtp:host=SAMSUNG_SAMSUNG_Android_R58N12345/Phone/DCIM/Camera/100% sharp #2?.jpg",
			scheme: "mtp", host: "SAMSUNG_SAMSUNG_Android_R58N12345", rel: "Phone/DCIM/Camera/100% sharp #2?.jpg",
			ok: true, device: true,
			uri: "mtp://SAMSUNG_SAMSUNG_Android_R58N12345/Phone/DCIM/Camera/100%25%20sharp%20%232%3F.jpg"},
		// Older gvfs: the USB address as host, already percent-encoded in
		// the directory name; it is kept as it is.
		{p: dir + "/mtp:host=%5Busb%3A002%2C005%5D/Card/DCIM/a.jpg",
			scheme: "mtp", host: "%5Busb%3A002%2C005%5D", rel: "Card/DCIM/a.jpg", ok: true, device: true,
			uri: "mtp://%5Busb%3A002%2C005%5D/Card/DCIM/a.jpg"},
		// Extra key=value parts after the host.
		{p: dir + "/gphoto2:host=Canon_EOS_R6,port=usb/store_00010001/DCIM/100CANON/IMG_1.CR3",
			scheme: "gphoto2", host: "Canon_EOS_R6", rel: "store_00010001/DCIM/100CANON/IMG_1.CR3", ok: true, device: true,
			uri: "gphoto2://Canon_EOS_R6/store_00010001/DCIM/100CANON/IMG_1.CR3"},
		// The mount directory itself.
		{p: dir + "/mtp:host=Google_Pixel_7_1A2B3C4D", scheme: "mtp", host: "Google_Pixel_7_1A2B3C4D", ok: true, device: true,
			uri: "mtp://Google_Pixel_7_1A2B3C4D/"},
		// A gvfs mount that is not a device.
		{p: dir + "/sftp:host=nas.local,user=me/photos/a.jpg", scheme: "sftp", host: "nas.local", rel: "photos/a.jpg",
			ok: true, uri: "sftp://nas.local/photos/a.jpg"},
		// No host key.
		{p: dir + "/smb-share:server=nas,share=photos/a.jpg"},
		{p: dir},
		{p: dir + "/"},
		{p: dir + "/plain/a.jpg"},
		{p: "/home/me/mtp:host=x/a.jpg"},
		{p: "/run/user/1000/gvfsx/mtp:host=x/a.jpg"},
		{p: dir + "/mtp:host=x/../y/a.jpg"},
		{p: "mtp:host=x/a.jpg"},
	}
	for _, c := range cases {
		scheme, host, rel, ok := parseMount(dir, c.p)
		if ok != c.ok || scheme != c.scheme || host != c.host || rel != c.rel {
			t.Errorf("parseMount(%q) = %q, %q, %q, %v; want %q, %q, %q, %v",
				c.p, scheme, host, rel, ok, c.scheme, c.host, c.rel, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if got := isDeviceScheme(scheme); got != c.device {
			t.Errorf("%q: device = %v", c.p, got)
		}
		if got := uriFor(scheme, host, rel); got != c.uri {
			t.Errorf("uriFor(%q) = %q, want %q", c.p, got, c.uri)
		}
	}
}

func TestDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/rt")
	if got := Dir(); got != filepath.Join("/tmp/rt", "gvfs") {
		t.Errorf("Dir() = %q", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := Dir(); filepath.Base(got) != "gvfs" || filepath.Dir(filepath.Dir(got)) != filepath.Join("/run", "user") {
		t.Errorf("Dir() without XDG_RUNTIME_DIR = %q", got)
	}
}
