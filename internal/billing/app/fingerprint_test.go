package app

import "testing"

func TestFingerprintUsesPointsNamespace(t *testing.T) {
	const want = "207671261f7c3ea44fbbc1445d04d8f82d20125c956263310898769a5ad58378"
	if got := fingerprint("wallet.adjust", 1, "10"); got != want {
		t.Fatalf("fingerprint() = %q, want %q", got, want)
	}
}

func TestRechargeCreateFingerprintKeepsLegacyAlipay(t *testing.T) {
	legacy := fingerprint("recharges.create", uint(7), "10.00")
	if got := rechargeCreateFingerprint(7, "10.00", ""); got != legacy {
		t.Fatalf("empty payment method fingerprint = %q, want legacy %q", got, legacy)
	}
	if got := rechargeCreateFingerprint(7, "10.00", "alipay"); got != legacy {
		t.Fatalf("alipay fingerprint = %q, want legacy %q", got, legacy)
	}
	if got := rechargeCreateFingerprint(7, "10.00", "epusdt_usdt_tron"); got == legacy {
		t.Fatalf("EPUSDT fingerprint unexpectedly reused legacy alipay fingerprint %q", got)
	}
}

func TestRechargeLegacyFingerprintsIncludePrePointsEpayReceipt(t *testing.T) {
	legacy := legacyFingerprint("recharges.create", uint(7), "10.00")
	got := rechargeLegacyFingerprints(7, "10000", "alipay")
	if len(got) != 1 || got[0] != legacy {
		t.Fatalf("legacy EPay fingerprints = %#v, want [%q]", got, legacy)
	}
	if got := rechargeLegacyFingerprints(7, "10.00", "epusdt_usdt_tron"); got != nil {
		t.Fatalf("EPUSDT unexpectedly accepted legacy EPay fingerprints: %#v", got)
	}
}

func TestRechargeLegacyFingerprintsSkipNonCentPointValues(t *testing.T) {
	if got := rechargeLegacyFingerprints(7, "10005", "alipay"); got != nil {
		t.Fatalf("legacy EPay fingerprints = %#v, want nil for non-cent quotient", got)
	}
}
