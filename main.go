// Command mimiban runs the radio-monitoring web app (`mimiban` / `mimiban serve`)
// or the remote streaming agent (`mimiban agent ...`).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/symysak/mimiban/internal/agent"
	"github.com/symysak/mimiban/internal/api"
	"github.com/symysak/mimiban/internal/asr"
	"github.com/symysak/mimiban/internal/config"
	"github.com/symysak/mimiban/internal/engine"
	"github.com/symysak/mimiban/internal/i18n"
	"github.com/symysak/mimiban/internal/notify"
	"github.com/symysak/mimiban/internal/store"
	"github.com/symysak/mimiban/web"
)

var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags)
	args := os.Args[1:]
	sub := "serve"
	if len(args) > 0 && (args[0] == "agent" || args[0] == "serve" || args[0] == "version") {
		sub = args[0]
		args = args[1:]
	}
	exe, _ := os.Executable()
	baseDir := filepath.Dir(exe)
	if wd, err := os.Getwd(); err == nil {
		// Prefer the working directory when it already holds a config.json or
		// runtime/ (typical when launched from the distribution folder).
		if _, err := os.Stat(filepath.Join(wd, "config.json")); err == nil {
			baseDir = wd
		} else if _, err := os.Stat(filepath.Join(wd, "runtime")); err == nil {
			baseDir = wd
		}
	}
	switch sub {
	case "version":
		fmt.Println("mimiban", version)
	case "agent":
		os.Exit(agent.Run(args, filepath.Join(baseDir, "runtime")))
	default:
		os.Exit(serve(args, baseDir))
	}
}

func serve(argv []string, baseDir string) int {
	fs := flag.NewFlagSet("mimiban serve", flag.ContinueOnError)
	dir := fs.String("dir", baseDir, "data directory (config.json, mimiban.sqlite3, recordings/, runtime/, models/)")
	host := fs.String("host", "", "override listen host")
	port := fs.Int("port", 0, "override listen port")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	baseDir = *dir
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		log.Printf("cannot create %s: %v", baseDir, err)
		return 1
	}
	cfgStore, err := config.Load(filepath.Join(baseDir, "config.json"))
	if err != nil {
		log.Printf("config: %v", err)
		return 1
	}
	if *host != "" || *port != 0 {
		_ = cfgStore.Update(func(c *config.Config) error {
			if *host != "" {
				c.Host = *host
			}
			if *port != 0 {
				c.Port = *port
			}
			return nil
		})
	}
	db, err := store.Open(filepath.Join(baseDir, "mimiban.sqlite3"))
	if err != nil {
		log.Printf("database: %v", err)
		return 1
	}
	defer db.Close()

	runtimeDir := filepath.Join(baseDir, "runtime")
	models := asr.NewModels(filepath.Join(baseDir, "models"))
	cfg := cfgStore.Get()

	srv := asr.NewServer(asr.ServerOptions{})
	client := asr.NewHTTPClient(srv.BaseURL)

	var notifier *notify.Sender
	baseURL := func() string {
		c := cfgStore.Get()
		if c.Notify.BaseURL != "" {
			return c.Notify.BaseURL
		}
		ip := api.TailscaleIP()
		if ip == "" {
			ip = "127.0.0.1"
		}
		return "http://" + ip + ":" + strconv.Itoa(c.Port)
	}
	notifier = notify.New(cfgStore.Get, i18n.Templates, baseURL, db)
	notifier.Logf = log.Printf
	notifier.Start()
	defer notifier.Stop()

	eng := engine.New(engine.Options{
		Config:      cfgStore,
		DB:          db,
		BaseDir:     baseDir,
		RuntimeDir:  runtimeDir,
		Models:      models,
		Transcriber: client,
		Server:      srv,
		Notifier:    notifier,
		Logf:        log.Printf,
	})
	ffm, whs, srt := eng.Binaries()
	log.Printf("Mimiban %s starting in %s", version, baseDir)
	log.Printf("runtime dir: %s", runtimeDir)
	if ffm.Found {
		log.Printf("ffmpeg: %s (%s), srt=%v", ffm.Path, ffm.Source, srt)
	} else {
		log.Printf("ffmpeg: NOT FOUND - recording is disabled until ffmpeg is placed in runtime/ or PATH")
	}
	if whs.Found {
		log.Printf("whisper-server: %s (%s)", whs.Path, whs.Source)
	} else {
		log.Printf("whisper-server: NOT FOUND - transcription disabled, recording continues")
	}
	if models.Path(cfg.ASRModel) == "" {
		log.Printf("model %q not present in models/ - download it from the settings tab", cfg.ASRModel)
	}
	eng.Start()
	defer eng.Stop()

	webFS, err := fsSub(web.FS)
	if err != nil {
		log.Printf("web: %v", err)
		return 1
	}
	apiSrv := &api.Server{Cfg: cfgStore, DB: db, Engine: eng, Models: models, Notifier: notifier, Web: webFS, Version: version}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	hs := &http.Server{Addr: addr, Handler: apiSrv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("listen %s: %v", addr, err)
		return 1
	}
	log.Printf("UI: http://%s/  (LAN: http://<this-host-ip>:%d/)", displayAddr(cfg.Host, cfg.Port), cfg.Port)
	if ip := api.TailscaleIP(); ip != "" {
		log.Printf("Tailscale: http://%s:%d/", ip, cfg.Port)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http: %v", err)
		}
	}()
	<-ctx.Done()
	log.Printf("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	return 0
}

func displayAddr(host string, port int) string {
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
