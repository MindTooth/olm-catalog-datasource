package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MindTooth/olm-catalog-datasource/internal/catalog"
	"github.com/MindTooth/olm-catalog-datasource/internal/service"
	"gopkg.in/yaml.v3"
)

type fileConfig struct {
	ListenAddress    string           `yaml:"listenAddress"`
	Debug            bool             `yaml:"debug"`
	RefreshInterval  string           `yaml:"refreshInterval"`
	RefreshTimeout   string           `yaml:"refreshTimeout"`
	SignaturePolicy  string           `yaml:"signaturePolicy"`
	ParseConcurrency int              `yaml:"parseConcurrency"`
	RefreshTokenFile string           `yaml:"refreshTokenFile"`
	Sources          []catalog.Source `yaml:"sources"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "refresh":
		refresh(os.Args[2:])
	case "query":
		query(os.Args[2:])
	case "channel-query":
		channelQuery(os.Args[2:])
	case "version":
		fmt.Println("olm-catalog-datasource dev")
	default:
		usage()
		os.Exit(2)
	}
}

func load(path string) (fileConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, err
	}
	return parseConfig(b)
}

func loadWithDigest(path string) (fileConfig, [sha256.Size]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, [sha256.Size]byte{}, err
	}
	c, err := parseConfig(b)
	return c, sha256.Sum256(b), err
}

func parseConfig(b []byte) (fileConfig, error) {
	var c fileConfig
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if len(c.Sources) == 0 {
		return c, errors.New("sources is required")
	}
	return c, nil
}
func toService(c fileConfig) (service.Config, error) {
	interval := 6 * time.Hour
	timeout := 30 * time.Minute
	var err error
	if c.RefreshInterval != "" {
		interval, err = time.ParseDuration(c.RefreshInterval)
		if err != nil {
			return service.Config{}, err
		}
	}
	if c.RefreshTimeout != "" {
		timeout, err = time.ParseDuration(c.RefreshTimeout)
		if err != nil {
			return service.Config{}, err
		}
	}
	seen := make(map[string]bool, len(c.Sources))
	for _, source := range c.Sources {
		if source.ID == "" || source.Image == "" {
			return service.Config{}, errors.New("every source requires id and image")
		}
		if seen[source.ID] {
			return service.Config{}, fmt.Errorf("duplicate source id %q", source.ID)
		}
		seen[source.ID] = true
	}
	return service.Config{Sources: c.Sources, RefreshInterval: interval, RefreshTimeout: timeout, SignaturePolicy: c.SignaturePolicy, ParseConcurrency: c.ParseConcurrency, RefreshTokenFile: c.RefreshTokenFile}, nil
}
func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "configuration file")
	listen := fs.String("listen", "", "HTTP listen address")
	debug := fs.Bool("debug", false, "enable debug logging")
	reloadInterval := fs.Duration("config-reload-interval", 5*time.Second, "configuration reload polling interval (0 disables reload)")
	_ = fs.Parse(args)
	if *reloadInterval < 0 {
		slog.Error("config reload interval must not be negative")
		os.Exit(2)
	}
	if *debug {
		setLogLevel(slog.LevelDebug)
	}
	c, configDigest, err := loadWithDigest(*configPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	if c.Debug {
		setLogLevel(slog.LevelDebug)
	}
	cfg, err := toService(c)
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}
	if *listen == "" {
		*listen = c.ListenAddress
	}
	if *listen == "" {
		*listen = ":8080"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	svc := service.New(cfg)
	go svc.Run(ctx)
	if *reloadInterval > 0 {
		go watchConfig(ctx, *configPath, *reloadInterval, configDigest, func(next fileConfig) {
			nextCfg, err := toService(next)
			if err != nil {
				slog.Error("reload config", "error", err)
				return
			}
			if next.ListenAddress != c.ListenAddress {
				slog.Warn("listen address change requires restart", "configured", next.ListenAddress)
			}
			if *debug || next.Debug {
				setLogLevel(slog.LevelDebug)
			} else {
				setLogLevel(slog.LevelInfo)
			}
			svc.Reload(nextCfg)
			c = next
			slog.Info("config reloaded", "sources", len(next.Sources))
		})
	}
	server := &http.Server{Addr: *listen, Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("serving", "address", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "error", err)
		os.Exit(1)
	}
}

// watchConfig compares file content instead of filesystem events. This also
// works for Kubernetes ConfigMap mounts, which atomically swap symlinks.
func watchConfig(ctx context.Context, configPath string, interval time.Duration, last [sha256.Size]byte, onChange func(fileConfig)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b, err := os.ReadFile(configPath)
			if err != nil {
				slog.Error("read config for reload", "error", err)
				continue
			}
			digest := sha256.Sum256(b)
			if digest == last {
				continue
			}
			last = digest
			c, err := parseConfig(b)
			if err != nil {
				slog.Error("reload config", "error", err)
				continue
			}
			onChange(c)
		}
	}
}

func setLogLevel(level slog.Level) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}
func refresh(args []string) {
	fs := flag.NewFlagSet("refresh", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "configuration file")
	sourceID := fs.String("source", "", "source ID")
	_ = fs.Parse(args)
	c, err := load(*configPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	cfg, err := toService(c)
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	svc := service.New(cfg)
	for _, source := range cfg.Sources {
		if *sourceID != "" && source.ID != *sourceID {
			continue
		}
		if err := svc.Refresh(context.Background(), source); err != nil {
			slog.Error("refresh", "source", source.ID, "error", err)
			os.Exit(1)
		}
		fmt.Println(source.ID)
	}
}
func query(args []string) {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	imageRef := fs.String("image", "", "catalog image")
	packageName := fs.String("package", "", "operator package")
	channel := fs.String("channel", "", "channel")
	currentVersion := fs.String("current-version", "", "installed version")
	currentBundle := fs.String("current-bundle", "", "installed bundle")
	policy := fs.String("signature-policy", "", "containers/image policy")
	platform := fs.String("platform", "", "catalog platform (for example linux/amd64)")
	_ = fs.Parse(args)
	if *imageRef == "" || *packageName == "" || (*currentVersion == "" && *currentBundle == "") {
		fs.Usage()
		os.Exit(2)
	}
	snap, err := (catalog.Reader{SignaturePolicy: *policy}).Read(context.Background(), catalog.Source{ID: "query", Image: *imageRef, Platform: *platform})
	if err != nil {
		slog.Error("read catalog", "error", err)
		os.Exit(1)
	}
	pkg := snap.Packages[*packageName]
	if pkg == nil {
		slog.Error("unknown package", "package", *packageName)
		os.Exit(1)
	}
	versions, err := pkg.VersionUpdates(catalog.UpdateRequest{CurrentVersion: *currentVersion, CurrentBundle: *currentBundle, Channel: *channel, Mode: "reachable"})
	if err != nil {
		slog.Error("resolve update", "error", err)
		os.Exit(1)
	}
	writeReleases(versions)
}

func channelQuery(args []string) {
	fs := flag.NewFlagSet("channel-query", flag.ExitOnError)
	imageRef := fs.String("image", "", "catalog image")
	packageName := fs.String("package", "", "operator package")
	currentChannel := fs.String("current-channel", "", "installed channel")
	currentVersion := fs.String("current-version", "", "installed version")
	currentBundle := fs.String("current-bundle", "", "installed bundle")
	policy := fs.String("signature-policy", "", "containers/image policy")
	platform := fs.String("platform", "", "catalog platform (for example linux/amd64)")
	_ = fs.Parse(args)
	if *imageRef == "" || *packageName == "" || *currentChannel == "" || (*currentVersion == "" && *currentBundle == "") {
		fs.Usage()
		os.Exit(2)
	}
	snap, err := (catalog.Reader{SignaturePolicy: *policy}).Read(context.Background(), catalog.Source{ID: "query", Image: *imageRef, Platform: *platform})
	if err != nil {
		slog.Error("read catalog", "error", err)
		os.Exit(1)
	}
	pkg := snap.Packages[*packageName]
	if pkg == nil {
		slog.Error("unknown package", "package", *packageName)
		os.Exit(1)
	}
	channels, err := pkg.ChannelUpdates(catalog.ChannelUpdateRequest{CurrentChannel: *currentChannel, CurrentVersion: *currentVersion, CurrentBundle: *currentBundle, Selection: "next"})
	if err != nil {
		slog.Error("resolve channel", "error", err)
		os.Exit(1)
	}
	writeReleases(channels)
}

func writeReleases(values []string) {
	out := struct {
		Releases []struct {
			Version string `json:"version"`
		} `json:"releases"`
	}{Releases: make([]struct {
		Version string `json:"version"`
	}, len(values))}
	for i, value := range values {
		out.Releases[i].Version = value
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		slog.Error("write response", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: olm-catalog-datasource {serve|refresh|query|channel-query|version}")
}
