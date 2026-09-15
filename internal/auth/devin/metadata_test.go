package devin

import "testing"

func TestGenerateDeviceFingerprint(t *testing.T) {
	fp1 := GenerateDeviceFingerprint("seed-1")
	if len(fp1) != devinFingerprintHexLen {
		t.Fatalf("fp1 len = %d, want %d", len(fp1), devinFingerprintHexLen)
	}
	// Check hex characters
	for _, c := range fp1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("invalid hex char in fingerprint: %c", c)
		}
	}

	// Deterministic seed produces deterministic fingerprint
	fp2 := GenerateDeviceFingerprint("seed-1")
	if fp1 != fp2 {
		t.Fatalf("fingerprints for same seed do not match: %s != %s", fp1, fp2)
	}
}

func TestGenerateDeviceFingerprint_RandomWhenEmptySeed(t *testing.T) {
	fp1 := GenerateDeviceFingerprint("")
	fp2 := GenerateDeviceFingerprint("")
	if len(fp1) != devinFingerprintHexLen {
		t.Fatalf("fp1 len = %d, want %d", len(fp1), devinFingerprintHexLen)
	}
	if len(fp2) != devinFingerprintHexLen {
		t.Fatalf("fp2 len = %d, want %d", len(fp2), devinFingerprintHexLen)
	}
	if fp1 == fp2 {
		t.Fatalf("fingerprints without explicit seed must be unique per call: %q == %q", fp1, fp2)
	}

	// With explicit seed, it must be deterministic
	seeded1 := GenerateDeviceFingerprint("my-stable-seed")
	seeded2 := GenerateDeviceFingerprint("my-stable-seed")
	if seeded1 != seeded2 {
		t.Fatalf("seeded fingerprints must be identical: %q != %q", seeded1, seeded2)
	}
}
