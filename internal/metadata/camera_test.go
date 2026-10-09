package metadata

import "testing"

func TestClassifyCamera(t *testing.T) {
	tests := []struct{ make, model, want string }{
		{"SONY", "ILCE-7M3", "camera"},
		{"Sony", "XQ-BC72", "phone"},
		{"Apple", "iPhone 12", "phone"},
		{"samsung", "SM-G991B", "phone"},
		{"SAMSUNG", "NX1", "camera"},
		{"Canon", "Canon EOS R5", "camera"},
		{"", "", "unknown"},
		{"HUAWEI", "ELE-L29", "phone"},
		{"NIKON CORPORATION", "NIKON D850", "camera"},
		{"FUJIFILM", "X-T4", "camera"},
		{"Google", "Pixel 7", "phone"},
		{"EASTMAN KODAK COMPANY", "KODAK EASYSHARE", "camera"},
		{"Acme", "Widget 3000", "unknown"},
		{"", "XT1092", "phone"},
		{"Sony", "G8341", "phone"},  // Xperia XZ1
		{"Sony", "F5121", "phone"},  // Xperia X
		{"Sony", "SO-01K", "phone"}, // docomo Xperia
		{"Sony", "DSC-RX100M7", "camera"},
		{"Microsoft", "Lumia 950", "phone"},
		{"Nokia", "Lumia 920", "phone"},
		{"Asus", "ASUS_I01WD", "phone"},
		{"LG", "Nexus 5", "phone"},
		{"Panasonic", "DC-GH5", "camera"},
		{"GoPro", "HERO9 Black", "camera"},
		{"Canon", "CanoScan 9000F", "camera"},
		{"HUAWEI", "VOG-L29", "phone"},
		{"FUJIFILM", "X100V", "camera"},
		{"", "Mi 9", "phone"},
	}
	for _, tt := range tests {
		if got := ClassifyCamera(tt.make, tt.model); got != tt.want {
			t.Errorf("ClassifyCamera(%q, %q) = %q, want %q", tt.make, tt.model, got, tt.want)
		}
	}
}
