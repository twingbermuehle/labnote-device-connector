// Command connector is the LabNote Device Connector: an on-premises agent that
// subscribes to OPC UA / LADS instruments and pushes finished measurements to
// LabNote's REST ingest API. Outbound HTTPS only.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/labnote/labnote-device-connector/internal/certs"
	"github.com/labnote/labnote-device-connector/internal/config"
	"github.com/labnote/labnote-device-connector/internal/heartbeat"
	"github.com/labnote/labnote-device-connector/internal/ingest"
	"github.com/labnote/labnote-device-connector/internal/keychain"
	"github.com/labnote/labnote-device-connector/internal/labnote"
	"github.com/labnote/labnote-device-connector/internal/logging"
	"github.com/labnote/labnote-device-connector/internal/manager"
	"github.com/labnote/labnote-device-connector/internal/outbox"
	"github.com/labnote/labnote-device-connector/internal/profiles"
	"github.com/labnote/labnote-device-connector/internal/state"
	"github.com/labnote/labnote-device-connector/internal/updater"
	"github.com/labnote/labnote-device-connector/internal/uploader"
	"github.com/labnote/labnote-device-connector/internal/webui"
)

// version is set at build time: -ldflags "-X main.version=v1.0.0".
var version = "dev"

func main() {
	var (
		showVersion = flag.Bool("version", false, "print the version and exit")
		debug       = flag.Bool("debug", false, "verbose logging")
		dataDir     = flag.String("data-dir", config.DataDir(), "directory for config, outbox, certificates and logs")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	// When Windows' service manager started us, hand control to it so the
	// service reports Running and survives the console window closing.
	handled, err := startService(func(ctx context.Context) error { return run(ctx, *dataDir, *debug) })
	if handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, "connector service failed:", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *dataDir, *debug); err != nil {
		fmt.Fprintln(os.Stderr, "connector failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dataDir string, debug bool) error {

	log, rotator, err := logging.New(filepath.Join(dataDir, "logs"), debug)
	if err != nil {
		return fmt.Errorf("open logs: %w", err)
	}
	log.Info("starting", "version", version, "data_dir", dataDir)

	cfgStore, err := config.Open(dataDir)
	if err != nil {
		return err
	}

	pki, err := certs.New(dataDir)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "labnote-connector"
	}
	if err := pki.EnsureClientCert("urn:"+host+":labnote:device-connector", host); err != nil {
		return fmt.Errorf("client certificate: %w", err)
	}
	if fp, err := pki.Fingerprint(); err == nil {
		log.Info("client certificate ready", "sha256", fp)
	}

	profs, err := profiles.Load(filepath.Join(dataDir, "profiles"))
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}

	box, err := outbox.Open(filepath.Join(dataDir, "outbox.db"))
	if err != nil {
		return fmt.Errorf("open outbox: %w", err)
	}
	defer box.Close()

	st := state.New()

	// The client is rebuilt per call so a changed URL or key takes effect
	// without restarting the service.
	clientFor := func() *labnote.Client {
		cfg := cfgStore.Get()
		if cfg.LabNoteURL == "" || !keychain.Has() {
			return nil
		}
		return labnote.New(cfg.LabNoteURL, keychain.Get, version)
	}

	mgr := manager.New(cfgStore, pki, profs, box, st, log)
	hb := heartbeat.New(cfgStore, st, clientFor, version, log)
	up := uploader.New(box, clientFor, st, log)
	upd := updater.New(version, func() bool { return cfgStore.Get().AutoUpdate }, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mgr.Start(ctx)
	go up.Run(ctx)
	go func() {
		if err := ingest.New(cfgStore, pki, box, st, log).ListenAndServe(ctx); err != nil {
			log.Error("push ingest failed", "error", err)
		}
	}()
	go hb.Run(ctx)
	go upd.Run(ctx)

	ui := webui.New(webui.Deps{
		Config: cfgStore, PKI: pki, Profiles: profs, State: st, Manager: mgr,
		Heartbeat: hb, Updater: upd, Logs: rotator, Client: clientFor,
		Version: version, Log: log,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- ui.ListenAndServe(ctx) }()

	if !cfgStore.Get().SetupComplete {
		log.Info("first run — open the setup interface to finish configuration", "url", "http://"+webui.Addr)
	}

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("setup interface: %w", err)
		}
	}

	log.Info("shutting down")
	mgr.Wait()
	return nil
}
