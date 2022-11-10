package main

import (
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/common/version"
	"gopkg.in/alecthomas/kingpin.v2"
	"net/http"
	"os"
)

func main() {
	app := kingpin.New("loki-label-proxy", "Proxy to enforce LogQL stream labels").Version(version.Print("loki-label-proxy"))
	app.HelpFlag.Short('h')
	addr := app.Flag("addr", "Server address. Can also be set using LOKI_ADDR env var.").Default("http://localhost:3100").Envar("LOKI_ADDR").String()
	lokiUser := app.Flag("loki-user", "Username for connection to Loki. Can also be set using LOKI_USERNAME env var.").Default("").Envar("LOKI_USERNAME").String()
	lokiPass := app.Flag("loki-pass", "Password for connection to Loki. Can also be set using LOKI_PASSWORD env var.").Default("").Envar("LOKI_PASSWORD").String()
	authUser := app.Flag("auth-user", "Username for HTTP basic auth. (Enables auth to proxy itself)").Default("").String()
	authPassSha := app.Flag("auth-pass-sha256", "sha256 of password for HTTP basic auth.").Default("").String()
	config := app.Flag("config", "Path to config files/dirs (repeated).").Default("/configs").Strings()
	logLevel := app.Flag("log", "Log filtering level (info, debug)").Default("info").Enum("error", "warn", "info", "debug")
	kingpin.MustParse(app.Parse(os.Args[1:]))

	logger := log.NewLogfmtLogger(os.Stdout)
	logger = level.NewFilter(logger, level.Allow(level.ParseDefault(*logLevel, level.InfoValue())))

	cfg := newConfig(config, logger)
	enforcer := newEnforcer(addr, lokiUser, lokiPass, authUser, authPassSha, cfg, logger)

	http.HandleFunc("/", enforcer.NotFound)
	http.HandleFunc("/healthz", enforcer.Health)
	// https://grafana.com/docs/loki/latest/api/
	http.HandleFunc("/loki/api/v1/label", enforcer.Pass)
	http.HandleFunc("/loki/api/v1/label/", enforcer.Pass)
	http.HandleFunc("/loki/api/v1/query", enforcer.Query)
	http.HandleFunc("/loki/api/v1/query_range", enforcer.Query)
	http.HandleFunc("/loki/api/v1/series", enforcer.Series)
	http.HandleFunc("/loki/api/v1/tail", enforcer.Query)
	http.HandleFunc("/loki/api/v1/index/stats", enforcer.Query)
	logger.Log(http.ListenAndServe(":8080", nil))
}
