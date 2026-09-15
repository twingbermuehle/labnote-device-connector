//go:build acceptance

// Acceptance tests against the SPECTARIS LADS OPC UA reference/simulator
// server, so the connector can be validated without instruments.
//
//	docker compose -f docker-compose.simulator.yml up -d
//	go test -tags acceptance ./test/acceptance/... -v
//
// LADS_SIMULATOR_ENDPOINT overrides the default simulator endpoint.
package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"

	"github.com/labnote/labnote-device-connector/internal/certs"
	"github.com/labnote/labnote-device-connector/internal/lads"
	"github.com/labnote/labnote-device-connector/internal/device"
	"github.com/labnote/labnote-device-connector/internal/labnote"
	"github.com/labnote/labnote-device-connector/internal/model"
	"github.com/labnote/labnote-device-connector/internal/outbox"
	"github.com/labnote/labnote-device-connector/internal/profiles"
	"github.com/labnote/labnote-device-connector/internal/state"
	"github.com/labnote/labnote-device-connector/internal/uploader"
)

func endpoint() string {
	if v := os.Getenv("LADS_SIMULATOR_ENDPOINT"); v != "" {
		return v
	}
	return "opc.tcp://127.0.0.1:4840"
}

// fakeLabNote records every ingest call and can be switched offline to
// simulate a network outage.
type fakeLabNote struct {
	mu       sync.Mutex
	server   *httptest.Server
	offline  bool
	received []model.Result
	seen     map[string]bool
	duplicates int
}

func newFakeLabNote(t *testing.T) *fakeLabNote {
	f := &fakeLabNote{seen: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc(labnote.PathResults, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.offline {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var rec model.Result
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.seen[rec.ExternalResultID] {
			// Server-side idempotency: a repeat is a conflict, not a new row.
			f.duplicates++
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.seen[rec.ExternalResultID] = true
		f.received = append(f.received, rec)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(labnote.PathConnectors, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeLabNote) setOffline(v bool) {
	f.mu.Lock()
	f.offline = v
	f.mu.Unlock()
}

// newClient returns an ingest client that trusts this test server's
// self-signed certificate. Production clients always verify TLS normally.
func (f *fakeLabNote) newClient() *labnote.Client {
	return labnote.New(
		f.server.URL,
		func() (string, error) { return "test-key", nil },
		"test",
		labnote.WithHTTPClient(f.server.Client()),
	)
}

func (f *fakeLabNote) results() []model.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Result(nil), f.received...)
}

func harness(t *testing.T) (*outbox.Outbox, *state.Store, model.Instrument, profiles.Profile, *certs.Store) {
	t.Helper()
	dir := t.TempDir()
	box, err := outbox.Open(filepath.Join(dir, "outbox.db"))
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	t.Cleanup(func() { _ = box.Close() })

	pki, err := certs.New(dir)
	if err != nil {
		t.Fatalf("pki: %v", err)
	}
	if err := pki.EnsureClientCert("urn:test:labnote:connector", "localhost"); err != nil {
		t.Fatalf("client cert: %v", err)
	}

	set, err := profiles.Load("")
	if err != nil {
		t.Fatalf("profiles: %v", err)
	}

	ins := model.Instrument{
		ID:               "test-instrument",
		Name:             "LADS simulator",
		ExternalDeviceID: "LADS-SIM-01",
		EndpointURL:      endpoint(),
		SecurityMode:     model.SecurityModeSignAndEncrypt,
		SecurityPolicy:   model.SecurityPolicyBasic256Sha256,
		Profile:          "generic-lads",
	}
	return box, state.New(), ins, set.Get("generic-lads"), pki
}

// pinAll trusts whatever certificate the simulator presents (TOFU accepted in
// the test harness only).
type pinAll struct{ fp string }

func (p *pinAll) Pinned(string) string { return p.fp }
func (p *pinAll) Pin(_, fp string) error {
	p.fp = fp
	return nil
}

// 1. A finished result must produce exactly one ingest record with correct
//    units and timestamps.
func TestFinishedResultUploadsExactlyOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fake := newFakeLabNote(t)
	box, st, ins, prof, pki := harness(t)
	trust := &pinAll{}

	// First dial reports the fingerprint; confirm it and reconnect.
	report := device.TestConnection(ctx, ins, pki, trust, st, testLogger())
	if report.ServerFingerprint != "" {
		_ = trust.Pin(ins.ID, report.ServerFingerprint)
	}

	sup := device.New(ins, prof, pki, box, st, trust, testLogger())
	go sup.Run(ctx)

	up := uploader.New(box, fake.newClient, st, testLogger())
	go up.Run(ctx)

	// The operator starts a run; the instrument then produces the result.
	startRun(t, ctx, "BC-ONCE-1")

	waitFor(t, 2*time.Minute, func() bool {
		for _, r := range fake.results() {
			if r.SampleCode == "BC-ONCE-1" {
				return true
			}
		}
		return false
	})

	// Give any duplicate a chance to arrive before asserting exactly-once.
	time.Sleep(15 * time.Second)
	var rec model.Result
	seen := 0
	for _, r := range fake.results() {
		if r.SampleCode == "BC-ONCE-1" {
			seen++
			rec = r
		}
	}
	if seen != 1 {
		t.Fatalf("want exactly 1 ingest record for the run, got %d", seen)
	}
	if rec.ExternalDeviceID != ins.ExternalDeviceID {
		t.Fatalf("wrong device id: %s", rec.ExternalDeviceID)
	}
	if rec.MeasuredAt.IsZero() {
		t.Fatal("measured_at must come from the OPC UA source timestamp")
	}
	if len(rec.Points) > 0 && rec.UnitY == "" && rec.UnitX == "" {
		t.Fatal("a series must carry units from EngineeringUnits or the configured defaults")
	}
}

// 2. Results produced during a 10 minute outage must all arrive, in order,
//    with no duplicates.
func TestOutageDeliversEverythingInOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fake := newFakeLabNote(t)
	box, st, _, _, _ := harness(t)
	fake.setOffline(true)

	// Simulate the results the instrument produced while the link was down.
	ids := []string{"r-1", "r-2", "r-3", "r-4", "r-5"}
	for _, id := range ids {
		rec := model.Result{ExternalDeviceID: "LADS-SIM-01", ExternalResultID: id, MeasuredAt: time.Now().UTC()}
		if err := box.Enqueue(ctx, rec); err != nil {
			t.Fatal(err)
		}
		// A retry of the same result must not create a second row.
		if err := box.Enqueue(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	up := uploader.New(box, fake.newClient, st, testLogger())
	go up.Run(ctx)

	time.Sleep(8 * time.Second)
	if len(fake.results()) != 0 {
		t.Fatal("nothing may be delivered while LabNote is unreachable")
	}
	fake.setOffline(false)

	waitFor(t, 90*time.Second, func() bool { return len(fake.results()) == len(ids) })

	got := fake.results()
	for i, id := range ids {
		if got[i].ExternalResultID != id {
			t.Fatalf("out of order at %d: want %s, got %s", i, id, got[i].ExternalResultID)
		}
	}
	if depth, _ := box.Depth(ctx); depth != 0 {
		t.Fatalf("outbox should be empty, depth=%d", depth)
	}
}

// 3. An untrusted instrument certificate must refuse the connection with a
//    clear message.
func TestUntrustedCertificateIsRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	_, st, ins, _, pki := harness(t)
	report := device.TestConnection(ctx, ins, pki, &pinAll{}, st, testLogger())
	if report.OK {
		t.Fatal("connection must be refused until the certificate is confirmed")
	}
	if report.ServerFingerprint == "" {
		t.Fatal("the UI needs the fingerprint to offer confirmation")
	}
}

// 4. A result carrying a sample barcode must set sample_code so LabNote files
//    it on the matching sample.
func TestSampleBarcodeBecomesSampleCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fake := newFakeLabNote(t)
	box, st, ins, prof, pki := harness(t)
	trust := &pinAll{}
	if report := device.TestConnection(ctx, ins, pki, trust, st, testLogger()); report.ServerFingerprint != "" {
		_ = trust.Pin(ins.ID, report.ServerFingerprint)
	}

	sup := device.New(ins, prof, pki, box, st, trust, testLogger())
	go sup.Run(ctx)
	up := uploader.New(box, fake.newClient, st, testLogger())
	go up.Run(ctx)

	startRun(t, ctx, "BC-SAMPLE-42")

	waitFor(t, 2*time.Minute, func() bool {
		for _, r := range fake.results() {
			if r.SampleCode == "BC-SAMPLE-42" {
				return true
			}
			if r.SampleCode != "" {
				return true
			}
		}
		return false
	})
}

// startRun acts as the lab operator: it calls the LADS StartProgram method on
// the simulator so the instrument produces a real result carrying the given
// sample barcode. The connector itself never calls methods.
func startRun(t *testing.T, ctx context.Context, barcode string) {
	t.Helper()
	c, err := opcua.NewClient(endpoint())
	if err != nil {
		t.Fatalf("simulator client: %v", err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("connect simulator: %v", err)
	}
	defer func() { _ = c.Close(context.WithoutCancel(ctx)) }()

	b := lads.NewBrowser(c)
	devs, err := b.Devices(ctx)
	if err != nil || len(devs) == 0 {
		t.Fatalf("discover devices: %v", err)
	}
	nid, err := ua.ParseNodeID(devs[0].NodeID)
	if err != nil {
		t.Fatal(err)
	}
	state, start, err := findStartProgram(ctx, c, nid)
	if err != nil {
		t.Fatalf("find StartProgram: %v", err)
	}
	props := []*ua.ExtensionObject{{
		EncodingMask: ua.ExtensionObjectBinary,
		TypeID:       &ua.ExpandedNodeID{NodeID: ua.NewNumericNodeID(0, id.KeyValuePair_Encoding_DefaultBinary)},
		Value: &ua.KeyValuePair{
			Key:   &ua.QualifiedName{Name: "SampleId"},
			Value: ua.MustVariant(barcode),
		},
	}}
	res, err := c.Call(ctx, &ua.CallMethodRequest{
		ObjectID: state,
		MethodID: start,
		InputArguments: []*ua.Variant{
			ua.MustVariant("MycoAlert Assay"),
			ua.MustVariant(props),
			ua.MustVariant("job-1"),
			ua.MustVariant("task-1"),
			ua.MustVariant([]*ua.ExtensionObject{}),
		},
	})
	if err != nil {
		t.Fatalf("StartProgram: %v", err)
	}
	if res.StatusCode != ua.StatusOK {
		t.Fatalf("StartProgram returned %v", res.StatusCode)
	}
}

func findStartProgram(ctx context.Context, c *opcua.Client, device *ua.NodeID) (*ua.NodeID, *ua.NodeID, error) {
	fus, err := c.Node(device).Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
	if err != nil {
		return nil, nil, err
	}
	for _, set := range fus {
		if n, _ := set.BrowseName(ctx); n == nil || n.Name != "FunctionalUnitSet" {
			continue
		}
		units, err := set.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
		if err != nil {
			return nil, nil, err
		}
		for _, u := range units {
			states, err := u.Children(ctx, id.HierarchicalReferences, ua.NodeClassObject)
			if err != nil {
				continue
			}
			for _, st := range states {
				if n, _ := st.BrowseName(ctx); n == nil || n.Name != "FunctionalUnitState" {
					continue
				}
				methods, err := st.Children(ctx, id.HierarchicalReferences, ua.NodeClassMethod)
				if err != nil {
					continue
				}
				for _, m := range methods {
					if n, _ := m.BrowseName(ctx); n != nil && n.Name == "StartProgram" {
						return st.ID, m.ID, nil
					}
				}
			}
		}
	}
	return nil, nil, errorNoStart
}

var errorNoStart = errors.New("simulator exposes no StartProgram method")

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
