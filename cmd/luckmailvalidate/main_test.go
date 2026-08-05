package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

func TestInputManifestAndPipeline(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(input, []byte("a@outlook.com----password----client----refresh\nb@outlook.com----password----client----refresh\nc@outlook.com----password----client----refresh\nd@outlook.com----password----client----refresh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	emails, err := loadEmails(input, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"b@outlook.com", "c@outlook.com"}; !reflect.DeepEqual(emails, want) {
		t.Fatalf("selected emails = %v, want %v", emails, want)
	}

	records := []manifestRecord{{Email: emails[0], ResourceID: 1, OwnerUserID: 2, OriginalForSale: true, ValidationGen: 3, CredentialRevision: 4, Eligible: true}}
	fallback, err := loadFallbackOAuthCredentials(input, records)
	if err != nil {
		t.Fatal(err)
	}
	if credential := fallback[emails[0]]; credential.ClientID != "client" || credential.RefreshToken != "refresh" {
		t.Fatalf("fallback credential = %#v", credential)
	}
	manifest := filepath.Join(dir, "manifest.tsv")
	if err := saveManifest(manifest, records); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, records) {
		t.Fatalf("loaded manifest = %#v, want %#v", loaded, records)
	}

	jobs := make([]manifestRecord, 100)
	queue := make(chan manifestRecord, 1)
	output := make(chan manifestRecord, 1)
	var workers sync.WaitGroup
	var processed atomic.Int64
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			for range output {
				processed.Add(1)
			}
		}()
	}
	done := make(chan struct{})
	go relayJobs(context.Background(), queue, output)
	go func() {
		sendJobs(context.Background(), jobs, queue)
		close(queue)
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("independent stage relay did not drain and close")
	}
	if processed.Load() != int64(len(jobs)) {
		t.Fatalf("processed %d jobs, want %d", processed.Load(), len(jobs))
	}
}

func TestFinalSaleStateRestoresManifestSupplyScope(t *testing.T) {
	if !strings.Contains(restoreForSaleStatement, "SET mr.for_sale = ?") {
		t.Fatal("final sale state must restore the manifest supply scope")
	}
	if strings.Contains(restoreForSaleStatement, "microsoft_allocations") {
		t.Fatal("active allocations must not turn a public resource private")
	}
}

func TestValidationProxyPoolLeasesUniqueRoutesAndRotatesAvoidedRoute(t *testing.T) {
	pool := newValidationProxyLeasePool(nil, []proxyapp.ProxyConfig{
		{ID: 1, ProxyServerID: 11, URL: "socks5://one.invalid", IPVersion: proxydomain.ProxyIPv4},
		{ID: 2, ProxyServerID: 22, URL: "socks5://two.invalid", IPVersion: proxydomain.ProxyIPv4},
		{ID: 3, ProxyServerID: 33, URL: "socks5://three.invalid", IPVersion: proxydomain.ProxyIPv4},
	})
	acquire := func(key string, avoided ...uint) *proxyapp.ProxyConfig {
		config, err := pool.Acquire(context.Background(), proxyapp.AcquireProxyRequest{
			Key: key, IPVersion: proxydomain.ProxyIPv4, AvoidProxyServerIDs: avoided,
		})
		if err != nil {
			t.Fatal(err)
		}
		return config
	}
	first := acquire("first@example.com")
	if sticky := acquire("first@example.com"); sticky.ID != first.ID {
		t.Fatalf("same resource changed proxy from %d to %d", first.ID, sticky.ID)
	}
	second := acquire("second@example.com")
	if second.ID == first.ID {
		t.Fatal("concurrent resources shared one IPv4 route")
	}
	rotated := acquire("first@example.com", first.ProxyServerID)
	if rotated.ID == first.ID || rotated.ID == second.ID {
		t.Fatalf("rotation selected a used or avoided route: %d", rotated.ID)
	}
	pool.Release("first@example.com")
	pool.Release("second@example.com")
	if len(pool.available) != pool.Capacity() {
		t.Fatalf("released routes = %d, want %d", len(pool.available), pool.Capacity())
	}
	used, rotations, peak := pool.Stats()
	if used != 3 || rotations != 1 || peak != 2 {
		t.Fatalf("pool stats = used:%d rotations:%d peak:%d", used, rotations, peak)
	}
}

func TestTrackerWritesRateLimitedFailuresTo429File(t *testing.T) {
	if !isRateLimitedSafeMessage("Microsoft authorization is temporarily rate limited.") || isRateLimitedSafeMessage("Microsoft authorization timed out.") {
		t.Fatal("rate-limit safe-message classification is incorrect")
	}
	dir := t.TempDir()
	tracker := newTracker(filepath.Join(dir, "error.txt"), map[string]struct{}{"previous@example.com": {}})
	tracker.fail("ordinary@example.com")
	tracker.fail("limited@example.com")
	tracker.markRateLimited("limited@example.com")
	tracker.succeed("success@example.com")

	if err := tracker.saveErrors(); err != nil {
		t.Fatal(err)
	}
	errorData, err := os.ReadFile(filepath.Join(dir, "error.txt"))
	if err != nil {
		t.Fatal(err)
	}
	rateLimitData, err := os.ReadFile(filepath.Join(dir, "429.txt"))
	if err != nil {
		t.Fatal(err)
	}
	successData, err := os.ReadFile(filepath.Join(dir, "success.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(errorData) != "limited@example.com\nordinary@example.com\n" {
		t.Fatalf("error.txt = %q", errorData)
	}
	if string(rateLimitData) != "limited@example.com\n" {
		t.Fatalf("429.txt = %q", rateLimitData)
	}
	if string(successData) != "previous@example.com\nsuccess@example.com\n" {
		t.Fatalf("success.txt = %q", successData)
	}
	completed, err := loadEmailSet(filepath.Join(dir, "success.txt"))
	if err != nil {
		t.Fatal(err)
	}
	remaining, skipped := excludeEmails([]string{"first@example.com", "success@example.com", "last@example.com"}, completed)
	if skipped != 1 || !reflect.DeepEqual(remaining, []string{"first@example.com", "last@example.com"}) {
		t.Fatalf("restart filter = %v, skipped %d", remaining, skipped)
	}
	if err := os.WriteFile(filepath.Join(dir, "invalid-success.txt"), []byte("not-an-email\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEmailSet(filepath.Join(dir, "invalid-success.txt")); err == nil {
		t.Fatal("invalid success ledger must stop the run")
	}
}
