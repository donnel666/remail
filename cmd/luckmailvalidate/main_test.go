package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	proxyapp "github.com/donnel666/remail/internal/proxy/app"
	proxydomain "github.com/donnel666/remail/internal/proxy/domain"
)

func TestValidationProxyFileParsesHostPortAndFullURLs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxies.txt")
	content := "\uFEFF# one proxy per line\n\n127.0.0.1:8080\nhttp://127.0.0.1:8080\nsocks5://user:password@127.0.0.2:1080\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	routes, err := loadValidationProxyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("proxy routes = %d, want 2", len(routes))
	}
	want := map[string]struct{}{
		"http://127.0.0.1:8080":                 {},
		"socks5://user:password@127.0.0.2:1080": {},
	}
	seenIDs := make(map[uint]struct{}, len(routes))
	for _, route := range routes {
		if _, ok := want[route.URL]; !ok {
			t.Fatalf("unexpected proxy URL %q", route.URL)
		}
		if route.ID == 0 || route.ProxyServerID != route.ID || route.IPVersion != proxydomain.ProxyIPv4 {
			t.Fatalf("invalid local proxy config = %#v", route)
		}
		if _, duplicate := seenIDs[route.ID]; duplicate {
			t.Fatalf("duplicate local proxy ID %d", route.ID)
		}
		seenIDs[route.ID] = struct{}{}
	}

	nextID := uint(1)
	_, err = parseValidationProxyRoutes(strings.NewReader("http://user:secret@127.0.0.1\n"), &nextID)
	if err == nil || !strings.Contains(err.Error(), "line 1") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe invalid-proxy error = %v", err)
	}
}

func TestCommandHistoryTriggerSchedulesBoundValidationOnce(t *testing.T) {
	output := make(chan manifestRecord, 2)
	trigger := newCommandHistoryTrigger(output)
	item := manifestRecord{Email: "owner@example.com", ResourceID: 42, Eligible: true}
	trigger.bind(item)

	if err := trigger.ScheduleValidatedMicrosoftHistory(context.Background(), item.ResourceID, "request-1"); err != nil {
		t.Fatal(err)
	}
	if err := trigger.enqueue(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if got := <-output; !reflect.DeepEqual(got, item) {
		t.Fatalf("history item = %#v, want %#v", got, item)
	}
	select {
	case duplicate := <-output:
		t.Fatalf("duplicate history item = %#v", duplicate)
	default:
	}
}

func TestValidationProxyRoutesInterleaveIPv4Subnets(t *testing.T) {
	rows := []validationProxyRoute{
		{ID: 1, OutboundIP: "10.1.1.1"},
		{ID: 2, OutboundIP: "10.1.1.2"},
		{ID: 3, OutboundIP: "10.1.1.3"},
		{ID: 4, OutboundIP: "10.1.1.4"},
		{ID: 5, OutboundIP: "10.2.2.1"},
		{ID: 6, OutboundIP: "10.2.2.2"},
		{ID: 7, OutboundIP: "10.3.3.1"},
		{ID: 8, OutboundIP: "10.3.3.2"},
	}

	ordered, subnetCount := interleaveValidationProxyRoutesBy24(rows)
	if subnetCount != 3 || len(ordered) != len(rows) {
		t.Fatalf("interleaved routes=%d subnets=%d, want %d/3", len(ordered), subnetCount, len(rows))
	}
	firstTwoRounds := make(map[string]int, subnetCount)
	for _, row := range ordered[:6] {
		firstTwoRounds[validationProxySubnetKey(row.OutboundIP)]++
	}
	for subnet, count := range firstTwoRounds {
		if count != 2 {
			t.Fatalf("subnet %s appeared %d times in first two rounds, want 2", subnet, count)
		}
	}
	if len(firstTwoRounds) != 3 {
		t.Fatalf("first two rounds used %d subnets, want 3", len(firstTwoRounds))
	}
	if got := validationProxySubnetKey("::ffff:192.0.2.9"); got != "192.0.2.0/24" {
		t.Fatalf("IPv4-mapped subnet = %q", got)
	}
}

func TestValidationProxyURLRefillsBelowThresholdAndUsesEachRouteOnce(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("num") != "3" {
			http.Error(w, "wrong batch size", http.StatusBadRequest)
			return
		}
		batch := calls.Add(1)
		for index := 0; index < 3; index++ {
			_, _ = fmt.Fprintf(w, "127.0.%d.%d:%d\n", batch, index+1, 8000+index)
		}
	}))
	defer server.Close()

	loader, err := newValidationProxyURLLoader(server.URL+"?region=US&num=150&time=15", 3)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := loader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pool := newRefillableValidationProxyLeasePool(routes, loader, 2)
	seenIDs := make(map[uint]struct{}, 3)
	for index := 0; index < 3; index++ {
		key := fmt.Sprintf("account-%d@example.com", index)
		leased, err := pool.Acquire(context.Background(), proxyapp.AcquireProxyRequest{
			Key: key, IPVersion: proxydomain.ProxyIPv4,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, reused := seenIDs[leased.ID]; reused {
			t.Fatalf("dynamic route %d was assigned more than once", leased.ID)
		}
		seenIDs[leased.ID] = struct{}{}
		pool.Release(key)
	}
	if calls.Load() != 2 {
		t.Fatalf("proxy source calls = %d, want 2", calls.Load())
	}
	if len(pool.available) != 3 {
		t.Fatalf("unused routes after refill = %d, want 3", len(pool.available))
	}
}

func TestValidationProxyURLSupportsIPRocketSocks5Text(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ips") != "3" || r.URL.Query().Get("num") != "" {
			http.Error(w, "wrong batch parameter", http.StatusBadRequest)
			return
		}
		for index := 0; index < 3; index++ {
			_, _ = fmt.Fprintf(w, "proxy-%d.invalid:9595:user:secret\n", index)
		}
	}))
	defer server.Close()

	loader, err := newValidationProxyURLLoader(server.URL+"?username=user&password=secret&ips=500&proxyType=Socks5&responseType=txt", 3)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := loader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 3 {
		t.Fatalf("IPRocket proxy routes = %d, want 3", len(routes))
	}
	for index, route := range routes {
		if !strings.HasPrefix(route.URL, "socks5://user:secret@proxy-") {
			t.Fatalf("IPRocket proxy route %d = %q", index, proxydomain.RedactProxyURL(route.URL))
		}
	}
}

func TestValidationProxyPoolDeduplicatesRoutesAcrossRefills(t *testing.T) {
	loader := func(context.Context) ([]proxyapp.ProxyConfig, error) {
		return []proxyapp.ProxyConfig{
			{ID: 2, ProxyServerID: 2, URL: "http://127.0.0.1:8080", IPVersion: proxydomain.ProxyIPv4},
			{ID: 3, ProxyServerID: 3, URL: "http://127.0.0.2:8080", IPVersion: proxydomain.ProxyIPv4},
		}, nil
	}
	p := newRefillableValidationProxyLeasePool([]proxyapp.ProxyConfig{{
		ID: 1, ProxyServerID: 1, URL: "http://127.0.0.1:8080", IPVersion: proxydomain.ProxyIPv4,
	}}, loader, 3)
	if err := p.refillRoutes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.available) != 2 || len(p.seenURLs) != 2 {
		t.Fatalf("cross-batch proxy dedupe available=%d seen=%d, want 2/2", len(p.available), len(p.seenURLs))
	}
	if err := p.refillRoutes(context.Background()); err == nil || !strings.Contains(err.Error(), "no unused proxies") {
		t.Fatalf("duplicate-only refill error = %v", err)
	}
	if len(p.available) != 2 || len(p.seenURLs) != 2 {
		t.Fatalf("duplicate-only refill changed pool available=%d seen=%d", len(p.available), len(p.seenURLs))
	}
}

func TestValidationProxyURLLoaderCombinesPartialBatches(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("num") != "3" {
			http.Error(w, "wrong batch size", http.StatusBadRequest)
			return
		}
		batch := calls.Add(1)
		for index := 0; index < 2; index++ {
			_, _ = fmt.Fprintf(w, "proxy-%d-%d.invalid:%d\n", batch, index, 8000+index)
		}
	}))
	defer server.Close()

	loader, err := newValidationProxyURLLoader(server.URL, 3)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := loader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 3 || calls.Load() != 2 {
		t.Fatalf("partial source batch routes=%d calls=%d, want routes=3 calls=2", len(routes), calls.Load())
	}
}

func TestValidationProxyURLErrorsDoNotExposeQuerySecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream failure", http.StatusInternalServerError)
	}))
	defer server.Close()
	loader, err := newValidationProxyURLLoader(server.URL+"?token=proxy-source-secret", 1000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader(context.Background())
	if err == nil || strings.Contains(err.Error(), "proxy-source-secret") {
		t.Fatalf("unsafe proxy source error = %v", err)
	}
}

func TestValidationProxyURLStopsFetchingAfterTrafficExhaustion(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("Insufficient traffic"))
	}))
	defer server.Close()

	loader, err := newValidationProxyURLLoader(server.URL, 3)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := loader(context.Background()); !errors.Is(err, errValidationProxySourceExhausted) {
			t.Fatalf("loader error = %v, want traffic exhaustion", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("exhausted proxy source fetched %d times, want 1", calls.Load())
	}

	pool := newRefillableValidationProxyLeasePool([]proxyapp.ProxyConfig{{
		ID: 1, ProxyServerID: 1, URL: "http://127.0.0.1:8080", IPVersion: proxydomain.ProxyIPv4,
	}}, loader, 2)
	leased, err := pool.Acquire(context.Background(), proxyapp.AcquireProxyRequest{Key: "first@example.com", IPVersion: proxydomain.ProxyIPv4})
	if err != nil || leased.ID != 1 {
		t.Fatalf("remaining route acquire = %#v, %v", leased, err)
	}
	pool.Release("first@example.com")
	if _, err := pool.Acquire(context.Background(), proxyapp.AcquireProxyRequest{Key: "second@example.com", IPVersion: proxydomain.ProxyIPv4}); !errors.Is(err, errValidationProxySourceExhausted) {
		t.Fatalf("drained pool error = %v, want traffic exhaustion", err)
	}
	if !pool.sourceExhaustedAndEmpty() {
		t.Fatal("drained dynamic proxy pool did not preserve exhaustion state")
	}
}

func TestValidationProxyPoolWaitsForLeasedRouteBeforeReportingSourceExhausted(t *testing.T) {
	loader := func(context.Context) ([]proxyapp.ProxyConfig, error) {
		return nil, errValidationProxySourceExhausted
	}
	p := newRefillableValidationProxyLeasePool([]proxyapp.ProxyConfig{{
		ID: 7, ProxyServerID: 7, URL: "http://127.0.0.1:8070", IPVersion: proxydomain.ProxyIPv4,
	}}, loader, 2)
	if _, err := p.Acquire(context.Background(), proxyapp.AcquireProxyRequest{Key: "held@example.com", IPVersion: proxydomain.ProxyIPv4}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := p.Acquire(context.Background(), proxyapp.AcquireProxyRequest{Key: "waiting@example.com", IPVersion: proxydomain.ProxyIPv4})
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("source exhaustion returned while a route was leased: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	p.Release("held@example.com")
	select {
	case err := <-result:
		if !errors.Is(err, errValidationProxySourceExhausted) {
			t.Fatalf("drained source error = %v, want exhaustion", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire did not finish after the last leased route was released")
	}
}

func TestRecoveryDispatchKeyUsesConcreteRecipientBeforeMask(t *testing.T) {
	msacl.SetAuxiliaryDomainPolicy([]string{"recovery.test"}, []string{"recovery.test"})
	item := manifestRecord{Email: "owner@outlook.com", ResourceID: 42}
	if got := recoveryDispatchKey(item, "Real.Recipient@Recovery.Test"); got != "real.recipient@recovery.test" {
		t.Fatalf("concrete recovery key = %q", got)
	}
	if got := recoveryDispatchKey(item, "om*****@recovery.test"); got != "om*****@recovery.test" {
		t.Fatalf("unresolved internal mask recovery key = %q", got)
	}
	if got := recoveryDispatchKey(item, "om*****@external.test"); got != "resource:42" {
		t.Fatalf("external mask recovery key = %q", got)
	}
	if got := recoveryDispatchKey(item, "real.recipient@external.test"); got != "resource:42" {
		t.Fatalf("external concrete recovery key = %q", got)
	}
	if got := recoveryDispatchKey(item, "not-an-address"); got != "resource:42" {
		t.Fatalf("fallback recovery key = %q", got)
	}
}

func TestStage1DispatcherSerializesOnlySameRecoveryKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := make(chan manifestRecord, 3)
	completions := make(chan stage1Completion, 3)
	output := make(chan manifestRecord)
	input <- manifestRecord{Email: "a1@example.com", ResourceID: 1, RecoveryKey: "same@recovery.test"}
	input <- manifestRecord{Email: "a2@example.com", ResourceID: 2, RecoveryKey: "same@recovery.test"}
	input <- manifestRecord{Email: "b1@example.com", ResourceID: 3, RecoveryKey: "other@recovery.test"}
	close(input)
	go dispatchStage1ByRecoveryKey(ctx, input, completions, output)

	first := <-output
	second := <-output
	if first.RecoveryKey == second.RecoveryKey {
		t.Fatalf("concurrent dispatch reused recovery key %q", first.RecoveryKey)
	}
	select {
	case item := <-output:
		t.Fatalf("same-key task dispatched before completion: %#v", item)
	case <-time.After(20 * time.Millisecond):
	}

	var sameActive, otherActive manifestRecord
	if first.RecoveryKey == "same@recovery.test" {
		sameActive, otherActive = first, second
	} else {
		sameActive, otherActive = second, first
	}
	completions <- stage1Completion{Item: sameActive, ActiveKey: sameActive.RecoveryKey}
	next := <-output
	if next.RecoveryKey != "same@recovery.test" || next.ResourceID == sameActive.ResourceID {
		t.Fatalf("same-key continuation = %#v", next)
	}
	completions <- stage1Completion{Item: otherActive, ActiveKey: otherActive.RecoveryKey}
	completions <- stage1Completion{Item: next, ActiveKey: next.RecoveryKey}
	select {
	case _, ok := <-output:
		if ok {
			t.Fatal("dispatcher produced an unexpected task")
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not drain")
	}
}

func TestStage1DispatcherDelaysRecoveryMailboxBusyRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := make(chan manifestRecord, 2)
	completions := make(chan stage1Completion, 2)
	output := make(chan manifestRecord)
	item := manifestRecord{Email: "busy@example.com", ResourceID: 4, RecoveryKey: "busy@recovery.test"}
	input <- item
	input <- manifestRecord{Email: "queued@example.com", ResourceID: 5, RecoveryKey: item.RecoveryKey}
	close(input)
	go dispatchStage1ByRecoveryKey(ctx, input, completions, output)

	active := <-output
	readyAt := time.Now().Add(40 * time.Millisecond)
	completions <- stage1Completion{Item: active, ActiveKey: active.RecoveryKey, Retry: true, RetryAt: readyAt}
	select {
	case <-output:
		t.Fatal("busy task was retried without backoff")
	case <-time.After(15 * time.Millisecond):
	}
	retried := <-output
	if retried.ResourceID != item.ResourceID || time.Now().Before(readyAt.Add(-5*time.Millisecond)) {
		t.Fatalf("busy retry = %#v at %s, ready at %s", retried, time.Now(), readyAt)
	}
	completions <- stage1Completion{Item: retried, ActiveKey: retried.RecoveryKey}
	queued := <-output
	if queued.ResourceID != 5 {
		t.Fatalf("same-key queue did not resume after busy retry: %#v", queued)
	}
	completions <- stage1Completion{Item: queued, ActiveKey: queued.RecoveryKey}
	select {
	case _, ok := <-output:
		if ok {
			t.Fatal("dispatcher produced an unexpected retry")
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not close after retry completion")
	}
}

func TestStage1WorkerStartupDelay(t *testing.T) {
	tests := []struct {
		worker   int
		interval time.Duration
		want     time.Duration
	}{
		{worker: 0, interval: 2 * time.Second, want: 0},
		{worker: 1, interval: 2 * time.Second, want: 2 * time.Second},
		{worker: 4, interval: 2 * time.Second, want: 8 * time.Second},
		{worker: 4, interval: 0, want: 0},
	}
	for _, test := range tests {
		if got := stage1WorkerStartupDelay(test.worker, test.interval); got != test.want {
			t.Fatalf("worker %d startup delay = %s, want %s", test.worker, got, test.want)
		}
	}
}

func TestInputManifestAndPipeline(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(input, []byte("a@outlook.com----password----client----refresh\nb@outlook.com----password----client----refresh\nc@outlook.com----password----client----refresh\nd@outlook.com----password----client----refresh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	emails, skipped, err := loadEmails(input, 1, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("unexpected skipped emails = %d", skipped)
	}
	if want := []string{"b@outlook.com", "c@outlook.com"}; !reflect.DeepEqual(emails, want) {
		t.Fatalf("selected emails = %v, want %v", emails, want)
	}

	records := []manifestRecord{{Email: emails[0], ResourceID: 1, OwnerUserID: 2, ValidationGen: 3, CredentialRevision: 4, Eligible: true}}
	fallback, err := loadFallbackOAuthCredentials(input, records, nil, nil)
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
	legacyManifest := filepath.Join(dir, "legacy-manifest.tsv")
	if err := os.WriteFile(legacyManifest, []byte("legacy@outlook.com\t9\t8\ttrue\t7\t6\ttrue\n"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := loadManifest(legacyManifest)
	if err != nil {
		t.Fatal(err)
	}
	if want := []manifestRecord{{Email: "legacy@outlook.com", ResourceID: 9, OwnerUserID: 8, ValidationGen: 7, CredentialRevision: 6, Eligible: true}}; !reflect.DeepEqual(legacy, want) {
		t.Fatalf("legacy manifest = %#v, want %#v", legacy, want)
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

func TestLoadEmailsSkipsPreviousSuccessBeforeRecordValidation(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.txt")
	content := "done@example.com----malformed\ndone@example.com----also-malformed\nrun@example.com----password----client----refresh\n"
	if err := os.WriteFile(input, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	emails, skipped, err := loadEmails(input, 2, 1, map[string]struct{}{"done@example.com": {}})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("selected-range skipped emails = %d, want 0", skipped)
	}
	if want := []string{"run@example.com"}; !reflect.DeepEqual(emails, want) {
		t.Fatalf("emails after success skip = %v, want %v", emails, want)
	}

	emails, skipped, err = loadEmails(input, 0, 2, map[string]struct{}{"done@example.com": {}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"run@example.com"}; !reflect.DeepEqual(emails, want) || skipped != 2 {
		t.Fatalf("success-filtered selection emails=%v skipped=%d", emails, skipped)
	}

	emails, skipped, err = loadEmails(input, 0, 1, map[string]struct{}{"done@example.com": {}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"run@example.com"}; !reflect.DeepEqual(emails, want) || skipped != 2 {
		t.Fatalf("success entries consumed the limit: emails=%v skipped=%d", emails, skipped)
	}
	emails, skipped, err = loadEmails(input, 0, 2, map[string]struct{}{
		"done@example.com": {}, "run@example.com": {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 0 || skipped != 3 {
		t.Fatalf("all-success input should be empty: emails=%v skipped=%d", emails, skipped)
	}
}

func TestFallbackCredentialsIgnoreNonWorkResourcesBeforeValidation(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(input, []byte("done@outlook.com----malformed\nskipped@outlook.com----also-malformed\nrun@outlook.com----password----client----refresh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	records := []manifestRecord{
		{Email: "done@outlook.com", ResourceID: 10, Eligible: true},
		{Email: "skipped@outlook.com", ResourceID: 11, Eligible: true},
		{Email: "run@outlook.com", ResourceID: 12, Eligible: true},
	}
	credentials, err := loadFallbackOAuthCredentials(input, records,
		map[string]struct{}{"done@outlook.com": {}},
		map[string]struct{}{"skipped@outlook.com": {}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if credential := credentials["run@outlook.com"]; credential.ClientID != "client" || credential.RefreshToken != "refresh" {
		t.Fatalf("selected fallback credentials = %#v", credential)
	}
	if len(credentials) != 1 {
		t.Fatalf("non-work fallback credentials = %#v", credentials)
	}

	credentials, err = loadFallbackOAuthCredentials(filepath.Join(dir, "missing.txt"), records,
		map[string]struct{}{"done@outlook.com": {}, "run@outlook.com": {}},
		map[string]struct{}{"skipped@outlook.com": {}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 0 {
		t.Fatalf("empty fallback credentials = %#v", credentials)
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

func TestValidationProxyPoolCoolsRateLimitedRoute(t *testing.T) {
	p := newValidationProxyLeasePool(nil, []proxyapp.ProxyConfig{
		{ID: 1, ProxyServerID: 11, URL: "socks5://one.invalid", IPVersion: proxydomain.ProxyIPv4},
		{ID: 2, ProxyServerID: 22, URL: "socks5://two.invalid", IPVersion: proxydomain.ProxyIPv4},
	})
	first, err := p.Acquire(context.Background(), proxyapp.AcquireProxyRequest{Key: "first@example.com", IPVersion: proxydomain.ProxyIPv4})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ReportFailure(context.Background(), first.ID, "Microsoft authorization is temporarily rate limited."); err != nil {
		t.Fatal(err)
	}
	p.Release("first@example.com")

	second, err := p.Acquire(context.Background(), proxyapp.AcquireProxyRequest{Key: "second@example.com", IPVersion: proxydomain.ProxyIPv4})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatalf("rate-limited route %d was immediately reused", first.ID)
	}
	p.Release("second@example.com")

	oneRoute := newValidationProxyLeasePool(nil, []proxyapp.ProxyConfig{{ID: 3, ProxyServerID: 33, URL: "socks5://three.invalid", IPVersion: proxydomain.ProxyIPv4}})
	leased, err := oneRoute.Acquire(context.Background(), proxyapp.AcquireProxyRequest{Key: "cooldown@example.com", IPVersion: proxydomain.ProxyIPv4})
	if err != nil {
		t.Fatal(err)
	}
	if err := oneRoute.ReportFailure(context.Background(), leased.ID, "Microsoft authorization is temporarily rate limited."); err != nil {
		t.Fatal(err)
	}
	oneRoute.Release("cooldown@example.com")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := oneRoute.Acquire(ctx, proxyapp.AcquireProxyRequest{Key: "retry@example.com", IPVersion: proxydomain.ProxyIPv4}); err == nil {
		t.Fatal("Acquire reused a route during its rate-limit cooldown")
	}
}

func TestValidationProxyPoolRateLimitedReportIsLocalAndPreRelease(t *testing.T) {
	p := newValidationProxyLeasePool(nil, []proxyapp.ProxyConfig{
		{ID: 41, ProxyServerID: 411, URL: "socks5://one.invalid", IPVersion: proxydomain.ProxyIPv4},
	})
	leased, err := p.Acquire(context.Background(), proxyapp.AcquireProxyRequest{
		Key: "limited@example.com", IPVersion: proxydomain.ProxyIPv4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ReportRateLimited(context.Background(), leased.ID); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	until, ok := p.cooldownUntil[leased.ID]
	p.mu.Unlock()
	if !ok || !until.After(time.Now()) {
		t.Fatalf("rate-limited route cooldown = %v, present=%v", until, ok)
	}
	if err := p.ReportSuccess(context.Background(), leased.ID); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	_, stillCooling := p.cooldownUntil[leased.ID]
	p.mu.Unlock()
	if !stillCooling {
		t.Fatal("later success reporting cleared an active rate-limit cooldown")
	}
	p.Release("limited@example.com")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Acquire(ctx, proxyapp.AcquireProxyRequest{
		Key: "next@example.com", IPVersion: proxydomain.ProxyIPv4,
	}); err == nil {
		t.Fatal("rate-limited route was reused before its local cooldown expired")
	}
}

func TestDynamicValidationProxyReleaseDropsDiscardedCooldown(t *testing.T) {
	p := newRefillableValidationProxyLeasePool([]proxyapp.ProxyConfig{{
		ID: 42, ProxyServerID: 421, URL: "socks5://dynamic.invalid", IPVersion: proxydomain.ProxyIPv4,
	}}, func(context.Context) ([]proxyapp.ProxyConfig, error) {
		return nil, errValidationProxySourceExhausted
	}, 1)
	leased, err := p.Acquire(context.Background(), proxyapp.AcquireProxyRequest{
		Key: "limited@example.com", IPVersion: proxydomain.ProxyIPv4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ReportRateLimited(context.Background(), leased.ID); err != nil {
		t.Fatal(err)
	}
	p.Release("limited@example.com")

	p.mu.Lock()
	_, cooling := p.cooldownUntil[leased.ID]
	p.mu.Unlock()
	if cooling {
		t.Fatal("discarded dynamic route retained a cooldown entry")
	}
}

func TestValidationProxyPoolHonorsProxyAttemptBudget(t *testing.T) {
	p := newValidationProxyLeasePool(nil, []proxyapp.ProxyConfig{{
		ID: 51, ProxyServerID: 511, URL: "socks5://one.invalid", IPVersion: proxydomain.ProxyIPv4,
	}})
	if _, err := p.Acquire(context.Background(), proxyapp.AcquireProxyRequest{
		Key: "budget@example.com", IPVersion: proxydomain.ProxyIPv4, Attempt: 3,
	}); err == nil {
		t.Fatal("proxy pool ignored the configured max_proxy_attempts budget")
	}
}

func TestTrackerWritesRateLimitedFailuresTo429File(t *testing.T) {
	if !isRateLimitedSafeMessage("Microsoft authorization is temporarily rate limited.") ||
		!isRateLimitedSafeMessage("rate_limited") ||
		isRateLimitedSafeMessage("Microsoft authorization timed out.") {
		t.Fatal("rate-limit safe-message classification is incorrect")
	}
	dir := t.TempDir()
	tracker := newTracker(filepath.Join(dir, "error.txt"), map[string]struct{}{"previous@example.com": {}}, nil, nil, nil)
	tracker.fail("ordinary@example.com", failureRecoverable)
	tracker.fail("permanent@example.com", failureUnrecoverable)
	tracker.fail("limited@example.com", failureRateLimited)
	tracker.fail("previous@example.com", failureRecoverable)
	tracker.succeed("previous@example.com")
	tracker.succeed("success@example.com")

	if err := tracker.saveErrors(); err != nil {
		t.Fatal(err)
	}
	errorData, err := os.ReadFile(filepath.Join(dir, "error.txt"))
	if err != nil {
		t.Fatal(err)
	}
	recoverableData, err := os.ReadFile(filepath.Join(dir, "recoverable.txt"))
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
	if string(errorData) != "permanent@example.com\n" {
		t.Fatalf("error.txt = %q", errorData)
	}
	if string(recoverableData) != "ordinary@example.com\n" {
		t.Fatalf("recoverable.txt = %q", recoverableData)
	}
	if string(rateLimitData) != "limited@example.com\n" {
		t.Fatalf("429.txt = %q", rateLimitData)
	}
	if string(successData) != "previous@example.com\nsuccess@example.com\n" {
		t.Fatalf("success.txt = %q", successData)
	}
	if tracker.success.Load() != 1 || tracker.failure.Load() != 3 {
		t.Fatalf("current-run accounting success=%d failure=%d", tracker.success.Load(), tracker.failure.Load())
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

func TestFailureClassForSafeMessageUsesExplicitTerminalEvidence(t *testing.T) {
	for _, message := range []string{
		"Microsoft refresh token is invalid or expired.",
		"Microsoft account password is incorrect.",
		"Microsoft account is restricted or requires recovery.",
		"Microsoft Graph verification did not complete after reauthorization.",
	} {
		if got := failureClassForSafeMessage(message); got != failureUnrecoverable {
			t.Fatalf("failure class for %q = %d, want unrecoverable", message, got)
		}
	}
	for _, message := range []string{
		"Microsoft authorization timed out.",
		"Microsoft authorization request failed temporarily.",
		"Auxiliary mailbox verification code was not received in time.",
		"Microsoft reauthorization returned incomplete OAuth credentials.",
		"Microsoft account authorization cleanup did not complete.",
		"Old-project identification did not complete.",
		"unknown future transient failure",
	} {
		if got := failureClassForSafeMessage(message); got != failureRecoverable {
			t.Fatalf("failure class for %q = %d, want recoverable", message, got)
		}
	}
	if got := failureClassForSafeMessage("Microsoft authorization is temporarily rate limited."); got != failureRateLimited {
		t.Fatalf("rate-limit failure class = %d", got)
	}
}

func TestTrackerPreservesPreviousFailureLedgersUntilClassified(t *testing.T) {
	dir := t.TempDir()
	previousSuccess := map[string]struct{}{"done@example.com": {}}
	previousRecoverable := map[string]struct{}{
		"ordinary@example.com": {},
	}
	previousUnrecoverable := map[string]struct{}{
		"permanent@example.com": {},
		"done@example.com":      {},
	}
	previousRateLimited := map[string]struct{}{
		"limited@example.com": {},
		"done@example.com":    {},
	}
	assertLedgers := func(wantErrors, wantRecoverable, wantRateLimited, wantSuccess string) {
		t.Helper()
		for name, want := range map[string]string{
			"error.txt":       wantErrors,
			"recoverable.txt": wantRecoverable,
			"429.txt":         wantRateLimited,
			"success.txt":     wantSuccess,
		} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != want {
				t.Fatalf("%s = %q, want %q", name, data, want)
			}
		}
	}

	tracker := newTracker(filepath.Join(dir, "error.txt"), previousSuccess, previousRecoverable, previousUnrecoverable, previousRateLimited)
	if err := tracker.saveErrors(); err != nil {
		t.Fatal(err)
	}
	assertLedgers(
		"permanent@example.com\n",
		"ordinary@example.com\n",
		"limited@example.com\n",
		"done@example.com\n",
	)

	tracker.succeed("limited@example.com")
	if err := tracker.saveErrors(); err != nil {
		t.Fatal(err)
	}
	assertLedgers(
		"permanent@example.com\n",
		"ordinary@example.com\n",
		"",
		"done@example.com\nlimited@example.com\n",
	)

	tracker = newTracker(filepath.Join(dir, "error.txt"), previousSuccess, previousRecoverable, previousUnrecoverable, previousRateLimited)
	tracker.fail("limited@example.com", failureRecoverable)
	if err := tracker.saveErrors(); err != nil {
		t.Fatal(err)
	}
	assertLedgers(
		"permanent@example.com\n",
		"limited@example.com\nordinary@example.com\n",
		"",
		"done@example.com\n",
	)
}

func TestResumeErrorSkipSetDefaultsTo429Only(t *testing.T) {
	previousRecoverable := map[string]struct{}{
		"ordinary@example.com": {},
	}
	previousUnrecoverable := map[string]struct{}{
		"permanent@example.com": {},
	}
	previousRateLimited := map[string]struct{}{
		"limited@example.com": {},
	}

	skipped := resumeErrorSkipSet(previousRecoverable, previousUnrecoverable, previousRateLimited, false, false)
	if !reflect.DeepEqual(skipped, map[string]struct{}{"ordinary@example.com": {}, "permanent@example.com": {}}) {
		t.Fatalf("default skipped errors = %#v", skipped)
	}
	recoverable := resumeErrorSkipSet(previousRecoverable, previousUnrecoverable, previousRateLimited, true, false)
	if !reflect.DeepEqual(recoverable, map[string]struct{}{"permanent@example.com": {}}) {
		t.Fatalf("recoverable skipped errors = %#v", recoverable)
	}
	if retryAll := resumeErrorSkipSet(previousRecoverable, previousUnrecoverable, previousRateLimited, false, true); len(retryAll) != 0 {
		t.Fatalf("retry-all skipped errors = %#v, want empty", retryAll)
	}
}

func TestFreshInputPolicySkipsSuccessAndUnrecoverable(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"success.txt":     "done@example.com\n",
		"error.txt":       "permanent@example.com\n",
		"recoverable.txt": "recoverable@example.com\n",
		"429.txt":         "limited@example.com\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	input := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(input, []byte("done@example.com\npermanent@example.com\nrecoverable@example.com\nlimited@example.com\nnew@example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}

	success, err := loadEmailSet(filepath.Join(dir, "success.txt"))
	if err != nil {
		t.Fatal(err)
	}
	recoverable, unrecoverable, rateLimited, found, err := loadSplitFailureLedgers(filepath.Join(dir, "error.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("split failure ledgers were not detected")
	}
	skippedErrors := resumeErrorSkipSet(recoverable, unrecoverable, rateLimited, true, false)
	emails, skipped, err := loadEmails(input, 0, 0, mergeEmailSets(success, skippedErrors))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"recoverable@example.com", "limited@example.com", "new@example.com"}; !reflect.DeepEqual(emails, want) || skipped != 2 {
		t.Fatalf("fresh input emails=%v skipped=%d, want %v skipped=2", emails, skipped, want)
	}
}

func TestClassifyJobsSkipsPreviousSuccessWithoutLoadingOrMutating(t *testing.T) {
	result := newTracker(filepath.Join(t.TempDir(), "error.txt"), nil, nil, nil, nil)
	manifest := []manifestRecord{{Email: "done@example.com", ResourceID: 10, Eligible: true}}
	previousSuccess := map[string]struct{}{"done@example.com": {}}

	stage1, stage2, err := classifyJobs(context.Background(), nil, manifest, 100, result, nil, previousSuccess)
	if err != nil {
		t.Fatal(err)
	}
	if len(stage1) != 0 || len(stage2) != 0 {
		t.Fatalf("previous success was dispatched: stage1=%d stage2=%d", len(stage1), len(stage2))
	}
	if result.success.Load() != 0 || result.failure.Load() != 0 {
		t.Fatalf("accounting success=%d failure=%d", result.success.Load(), result.failure.Load())
	}
}

func TestCollectStage1ManifestExcludesStage2AndNonWorkResources(t *testing.T) {
	historyOnly := []manifestRecord{{Email: "history@example.com", ResourceID: 1, Eligible: true}}
	if got := collectStage1Manifest(historyOnly, len(historyOnly), nil, nil, nil); len(got) != 0 {
		t.Fatalf("history-only resume requires stage 1: %#v", got)
	}

	manifest := []manifestRecord{
		{Email: "done@example.com", ResourceID: 2, Eligible: true},
		{Email: "skipped@example.com", ResourceID: 3, Eligible: true},
		{Email: "missing@example.com"},
		{Email: "run@example.com", ResourceID: 4, Eligible: true},
	}
	got := collectStage1Manifest(manifest, 0, nil,
		map[string]struct{}{"skipped@example.com": {}},
		map[string]struct{}{"done@example.com": {}},
	)
	if want := []manifestRecord{manifest[3]}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stage 1 manifest = %#v, want %#v", got, want)
	}
}

func TestRecoverAbandonedValidationsSkipsPreviousSuccess(t *testing.T) {
	manifest := []manifestRecord{{Email: "done@example.com", ResourceID: 10, Eligible: true}}
	previousSuccess := map[string]struct{}{"done@example.com": {}}

	if err := recoverAbandonedValidations(context.Background(), nil, manifest, 100, nil, previousSuccess); err != nil {
		t.Fatal(err)
	}
}

func TestFreezeAndFeedSkipsPreviousSuccessWithoutFreezing(t *testing.T) {
	dir := t.TempDir()
	result := newTracker(filepath.Join(dir, "error.txt"), nil, nil, nil, nil)
	state := checkpoint{Total: 1}
	cfg := config{chunkSize: 10, pendingCap: 10, statePath: filepath.Join(dir, "state.json")}
	manifest := []manifestRecord{{Email: "done@example.com", ResourceID: 10, Eligible: true}}
	previousSuccess := map[string]struct{}{"done@example.com": {}}
	output := make(chan manifestRecord, 1)

	if err := freezeAndFeed(context.Background(), nil, manifest, nil, cfg, &state, result, make(chan struct{}, 10), output, nil, previousSuccess); err != nil {
		t.Fatal(err)
	}
	if state.FreezeOffset != 1 || result.success.Load() != 0 || result.failure.Load() != 0 {
		t.Fatalf("skipped state offset=%d success=%d", state.FreezeOffset, result.success.Load())
	}
	if _, ok := <-output; ok {
		t.Fatal("previous success was sent to a worker")
	}
}
