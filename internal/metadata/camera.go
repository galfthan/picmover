package metadata

import (
	"regexp"
	"strings"
)

var (
	phoneModelContains = []string{
		"iphone", "galaxy", "pixel", "xperia", "redmi", "poco", "mi ", "nexus",
		"zenfone", "smartphone", "phone", "lumia",
	}
	phoneModelPrefixes = []string{"sm-", "lg-", "xq-", "lm-", "moto ", "so-", "asus_"}
	phoneModelRe       = regexp.MustCompile(`^(SM-|LG-|XT\d{4}|FRD-|LE\d{4}|AC\d{4})`)
	// Sony Xperia numeric model names such as G8341 or F5121.
	xperiaModelRe = regexp.MustCompile(`^[EFGHJ]\d{4}$`)

	cameraModelContains = []string{
		"ilce", "dsc-", "eos", "nikon d", "x-t", "x-e", "x-pro", "gfx", "dmc-",
		"dc-", "e-m", "om-", "pen-",
	}
	cameraModelPrefixes = []string{"nx"}

	phoneMakes = []string{
		"apple", "samsung", "huawei", "xiaomi", "oppo", "vivo", "oneplus",
		"motorola", "nokia", "htc", "google", "lenovo", "alcatel", "zte",
		"blackberry", "meizu", "realme", "hmd global", "honor", "lg electronics",
		"lge", "tecno", "infinix", "fairphone", "nothing",
	}
	cameraMakes = []string{
		"canon", "nikon", "sony", "fujifilm", "olympus", "om digital", "panasonic",
		"leica", "hasselblad", "pentax", "ricoh", "kodak", "gopro", "dji", "sigma",
		"phase one", "minolta", "konica", "casio",
	}
)

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ClassifyCamera returns "phone", "camera" or "unknown".
//
// Model hints are checked before the make, so that e.g. a Sony Xperia is a
// phone while a Sony Alpha (ILCE-*) is a camera, and a Samsung NX is a camera.
func ClassifyCamera(make, model string) string {
	model = strings.TrimSpace(model)
	lm := strings.ToLower(model)
	lmk := strings.ToLower(strings.TrimSpace(make))

	if lm != "" {
		if containsAny(lm, phoneModelContains) || hasAnyPrefix(lm, phoneModelPrefixes) || phoneModelRe.MatchString(model) || xperiaModelRe.MatchString(model) {
			return "phone"
		}
		if containsAny(lm, cameraModelContains) || hasAnyPrefix(lm, cameraModelPrefixes) {
			return "camera"
		}
	}
	if lmk != "" {
		if containsAny(lmk, phoneMakes) {
			return "phone"
		}
		if containsAny(lmk, cameraMakes) {
			return "camera"
		}
	}
	return "unknown"
}
