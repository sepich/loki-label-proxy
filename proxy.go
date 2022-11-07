package main

import (
	"fmt"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/loki/pkg/logql/syntax"
	"github.com/prometheus/prometheus/model/labels"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

type Enforcer struct {
	logger      log.Logger
	username    *string
	password    *string
	target      *url.URL
	config      *Config
	proxyPass   *httputil.ReverseProxy
	proxyQuery  *httputil.ReverseProxy
	proxySeries *httputil.ReverseProxy
}

func newEnforcer(addr *string, username *string, password *string, cfg *Config, logger log.Logger) *Enforcer {
	target, err := url.Parse(*addr)
	if err != nil {
		logger.Log("msg", "Unable to parse addr as url", "error", err)
		os.Exit(1)
	}
	level.Info(logger).Log("msg", fmt.Sprintf("Listening on :8080, forwarding to Loki upstream: %s://%s", target.Scheme, target.Host))

	e := &Enforcer{
		target:   target,
		username: username,
		password: password,
		logger:   logger,
		config:   cfg,
	}
	e.proxyPass = e.proxyFactory("")
	e.proxyQuery = e.proxyFactory("query")
	e.proxySeries = e.proxyFactory("match[]")
	return e
}

func (e *Enforcer) NotFound(w http.ResponseWriter, r *http.Request) {
	level.Debug(e.logger).Log("msg", "NotFound", "request", dumpReq(r, false))
	w.WriteHeader(http.StatusNotFound)
}

func (e *Enforcer) Health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (e *Enforcer) Pass(w http.ResponseWriter, r *http.Request) {
	e.proxyPass.ServeHTTP(w, r)
}

func (e *Enforcer) Query(w http.ResponseWriter, r *http.Request) {
	e.proxyQuery.ServeHTTP(w, r)
}

func (e *Enforcer) Series(w http.ResponseWriter, r *http.Request) {
	e.proxySeries.ServeHTTP(w, r)
}

// https://github.com/golang/go/issues/34733
// It is used to cancel the request to upstream when Director sets the X-Routing-Error header
type roundTripperFilter struct {
	parent http.RoundTripper
}

func (rtf *roundTripperFilter) RoundTrip(r *http.Request) (*http.Response, error) {
	if err, ok := r.Header["X-Routing-Error"]; ok {
		return nil, fmt.Errorf("%s", err)
	}
	return rtf.parent.RoundTrip(r)
}

func (e *Enforcer) proxyFactory(enforce string) *httputil.ReverseProxy {
	// based on httputil.NewSingleHostReverseProxy()
	director := func(req *http.Request) {
		level.Info(e.logger).Log("request", dumpReq(req, false))
		req.URL.Scheme = e.target.Scheme
		req.URL.Host = e.target.Host
		req.Host = e.target.Host
		if e.username != nil && e.password != nil {
			req.SetBasicAuth(*e.username, *e.password)
		}

		assign, err := e.lookupUser(req)
		if err != nil {
			level.Info(e.logger).Log("msg", "Request denied", "error", err)
			req.Header.Set("X-Routing-Error", err.Error())
			return
		}
		if err := rewriteReq(enforce, req, assign, e.logger); err != nil {
			return
		}

		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
		level.Debug(e.logger).Log("enforce", enforce, "request", dumpReq(req, true))
	}
	return &httputil.ReverseProxy{
		Director:  director,
		Transport: &roundTripperFilter{parent: http.DefaultTransport},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if s, ok := req.Header["X-Routing-Error"]; ok {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(strings.Join(s, ",") + "\n"))
				return
			}
		},
	}
}

func (e *Enforcer) lookupUser(req *http.Request) (labels.Labels, error) {
	org := req.Header.Get("X-Grafana-Org-Id")
	if org == "" {
		return nil, fmt.Errorf("X-Grafana-Org-Id header not found in request")
	}
	user := req.Header.Get("X-Grafana-User")
	if user == "" {
		return nil, fmt.Errorf("X-Grafana-User header not found in request")
	}
	if _, ok := e.config.orgs[org]; !ok {
		return nil, fmt.Errorf("X-Grafana-Org-Id %s not found in configs", org)
	}
	m := e.config.orgs[org].Users["default"]
	if u, ok := e.config.orgs[org].Users[user]; ok {
		m = u
	}
	assign := make(labels.Labels, 0, len(m))
	for k, v := range m {
		assign = append(assign, labels.Label{Name: k, Value: v})
	}
	return assign, nil
}

// Parse LogQL and add labels to series selector.
// It is parseError if query does not have series selector.
func logqlLabels(logql string, assign labels.Labels) (string, error) {
	logql = strings.TrimSpace(logql)

	parsed, err := syntax.ParseExpr(logql)
	if err != nil {
		return "", err
	}
	parsed.Walk(func(x interface{}) {
		switch me := x.(type) {
		case *syntax.MatchersExpr:
			for _, l := range assign {
				me.AppendMatchers([]*labels.Matcher{{
					Name:  l.Name,
					Type:  labels.MatchRegexp,
					Value: l.Value,
				}})
			}
		default:
			// Do nothing
		}
	})
	return parsed.String(), nil
}

// dumpReq pretty prints the request for logging
func dumpReq(req *http.Request, debug bool) string {
	if debug {
		dump, _ := httputil.DumpRequestOut(req, true)
		return string(dump)
	}

	d := ""
	if len(req.URL.RawQuery) > 0 {
		d = "?"
	}
	qs, err := url.QueryUnescape(req.URL.RawQuery)
	if err != nil {
		qs = req.URL.RawQuery
	}
	// to dump POST need to read the body
	return fmt.Sprintf("%s %s%s%s", req.Method, req.URL.Path, d, qs)
}

// rewriteReq set/add the field enforce in GET/POST request
func rewriteReq(enforce string, req *http.Request, assign labels.Labels, logger log.Logger) error {
	if enforce != "" {
		if req.Method == "GET" {
			qs := req.URL.Query()
			if err := rewriteField(enforce, req, &qs, assign, logger); err != nil {
				return err
			}
			req.URL.RawQuery = qs.Encode()
		} else if req.Method == "POST" {
			req.ParseForm()
			if err := rewriteField(enforce, req, &req.Form, assign, logger); err != nil {
				return err
			}
			data := req.Form.Encode()
			req.Body = io.NopCloser(strings.NewReader(data))
			req.ContentLength = int64(len(data))
		} else {
			level.Error(logger).Log("msg", "unsupported method", "method", req.Method)
			req.Header["X-Routing-Error"] = []string{fmt.Sprintf("Unsupported method %s", req.Method)}
			return fmt.Errorf(req.Header["X-Routing-Error"][0])
		}
	}
	return nil
}

// rewriteField rewrites field in the query values, or adds if it is empty/not-set
func rewriteField(field string, req *http.Request, form *url.Values, assign labels.Labels, logger log.Logger) error {
	if _, ok := (*form)[field]; ok {
		for i, m := range (*form)[field] {
			if q, err := logqlLabels(m, assign); err == nil {
				(*form)[field][i] = q
			} else {
				level.Error(logger).Log("msg", "failed to parse", field, m, "error", err)
				req.Header["X-Routing-Error"] = []string{err.Error()}
				return err
			}
		}
	} else {
		level.Error(logger).Log("msg", fmt.Sprintf("%s is empty, assigning labels", field))
		x := syntax.MatchersExpr{}
		for _, l := range assign {
			x.AppendMatchers([]*labels.Matcher{{
				Name:  l.Name,
				Type:  labels.MatchRegexp,
				Value: l.Value,
			}})
		}
		(*form)[field] = []string{x.String()}
	}

	return nil
}
