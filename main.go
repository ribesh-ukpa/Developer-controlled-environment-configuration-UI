package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

//go:embed web
var webFS embed.FS

// Config holds all runtime settings, sourced entirely from environment
// variables so the binary stays stateless and 12-factor friendly.
type Config struct {
	Port              string
	BasicAuthUser     string
	BasicAuthPass     string
	AllowedNamespaces []string // hard allow-list; anything else is refused
	AllowSecrets      bool     // when false, the Secrets tab is disabled server-side
}

// App wires together config, the Kubernetes client and the audit logger.
type App struct {
	cfg   Config
	k8s   kubernetes.Interface
	audit *log.Logger
}

func loadConfig() Config {
	cfg := Config{
		Port:          getenv("APP_PORT", "8080"),
		BasicAuthUser: getenv("BASIC_AUTH_USER", "developer"),
		BasicAuthPass: os.Getenv("BASIC_AUTH_PASS"),
		AllowSecrets:  getenv("ALLOW_SECRETS", "true") == "true",
	}

	for _, ns := range strings.Split(getenv("ALLOWED_NAMESPACES", "dev"), ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			cfg.AllowedNamespaces = append(cfg.AllowedNamespaces, ns)
		}
	}

	if cfg.BasicAuthPass == "" {
		log.Fatal("BASIC_AUTH_PASS must be set (mount it from a Kubernetes Secret)")
	}
	if len(cfg.AllowedNamespaces) == 0 {
		log.Fatal("ALLOWED_NAMESPACES must list at least one namespace")
	}
	return cfg
}

func main() {
	cfg := loadConfig()

	clientset, err := buildKubeClient()
	if err != nil {
		log.Fatalf("failed to build kubernetes client: %v", err)
	}

	app := &App{
		cfg:   cfg,
		k8s:   clientset,
		audit: log.New(os.Stdout, "AUDIT ", 0), // structured JSON written below
	}

	mux := http.NewServeMux()

	// Unauthenticated liveness/readiness probe.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// API (Go 1.22 method+pattern routing — no router dependency).
	mux.HandleFunc("GET /api/namespaces", app.handleNamespaces)
	mux.HandleFunc("GET /api/resources/{ns}/{kind}", app.handleList)
	mux.HandleFunc("GET /api/resources/{ns}/{kind}/{name}", app.handleGet)
	mux.HandleFunc("PUT /api/resources/{ns}/{kind}/{name}", app.handleUpdate)

	// Static UI, embedded into the binary.
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	handler := app.withBasicAuth(mux)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("listening on :%s | namespaces=%v | secrets=%v",
		cfg.Port, cfg.AllowedNamespaces, cfg.AllowSecrets)
	log.Fatal(srv.ListenAndServe())
}

// auditEntry is one immutable record of a mutating action, emitted as JSON
// to stdout so the cluster log pipeline captures it durably.
type auditEntry struct {
	Time        string   `json:"time"`
	DevName     string   `json:"devName"` // self-reported (shared-secret auth)
	RemoteAddr  string   `json:"remoteAddr"`
	Action      string   `json:"action"`
	Namespace   string   `json:"namespace"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	ChangedKeys []string `json:"changedKeys"`
}

func (a *App) logAudit(e auditEntry) {
	e.Time = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(e)
	a.audit.Println(string(b))
}

// buildKubeClient picks how to reach the cluster:
//  1. In-cluster ServiceAccount token — how it runs when deployed as a Pod.
//  2. A kubeconfig (honours $KUBECONFIG, else ~/.kube/config) — how it runs
//     locally or inside a test container against a dev/local cluster.
//
// NOTE: in kubeconfig mode the app acts with *your* credentials, so the
// deploy/rbac.yaml restrictions do NOT apply — only the in-app
// ALLOWED_NAMESPACES / ALLOW_SECRETS guards do. Fine for testing.
func buildKubeClient() (kubernetes.Interface, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		log.Println("kube: using in-cluster ServiceAccount")
		return kubernetes.NewForConfig(cfg)
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("no in-cluster token and no usable kubeconfig: %w", err)
	}
	log.Printf("kube: using kubeconfig (%s)", rules.GetLoadingPrecedence())
	return kubernetes.NewForConfig(cfg)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// writeJSON / writeErr are tiny helpers for consistent responses.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
