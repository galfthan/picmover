package geo

import "testing"

func TestParseISO6709(t *testing.T) {
	lat, lon, alt, hasAlt, ok := ParseISO6709("+60.1699+024.9384+010.000/")
	if !ok || lat != 60.1699 || lon != 24.9384 || !hasAlt || alt != 10 {
		t.Errorf("got %v %v %v %v %v", lat, lon, alt, hasAlt, ok)
	}
	lat, lon, _, hasAlt, ok = ParseISO6709("-33.8688+151.2093/")
	if !ok || lat != -33.8688 || lon != 151.2093 || hasAlt {
		t.Errorf("got %v %v %v %v", lat, lon, hasAlt, ok)
	}
	if _, _, _, _, ok := ParseISO6709("+95.0+10.0/"); ok {
		t.Error("accepted out-of-range latitude")
	}
	if _, _, _, _, ok := ParseISO6709("garbage"); ok {
		t.Error("accepted garbage")
	}
}

func TestParseLatLon(t *testing.T) {
	lat, lon, ok := ParseLatLon("60.169900,24.938400")
	if !ok || lat != 60.1699 || lon != 24.9384 {
		t.Errorf("got %v %v %v", lat, lon, ok)
	}
	if _, _, ok := ParseLatLon(""); ok {
		t.Error("accepted empty")
	}
	if _, _, ok := ParseLatLon("1,2,3"); ok {
		t.Error("accepted three parts")
	}
}
