package fasthttpprometheus

import (
	"strconv"
	"sync"
	"time"

	"github.com/buaazp/fasthttprouter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

var (
	defaultMetricPath  = "/metrics"
	requestHandlerPool sync.Pool
)

type FasthttpHandlerFunc func(*fasthttp.RequestCtx)

type Prometheus struct {
	reqCnt            *prometheus.CounterVec
	reqDur            *prometheus.HistogramVec
	reqSize, respSize prometheus.Summary
	router            *fasthttprouter.Router
	reqConcurrent     prometheus.Gauge

	registry  *prometheus.Registry
	subsystem string

	MetricsPath string
}

func NewPrometheus(options ...func(*Prometheus)) *Prometheus {

	p := &Prometheus{
		MetricsPath: defaultMetricPath,
	}

	for _, option := range options {
		option(p)
	}

	p.registerMetrics()

	return p
}

// Registry is an option allowing to set a  *prometheus.Registry with New
func Registry(r *prometheus.Registry) func(*Prometheus) {
	return func(p *Prometheus) {
		p.registry = r
	}
}

// Subsystem is an option which allows to set the subsystem when initializing with New
func Subsystem(sub string) func(*Prometheus) {
	return func(p *Prometheus) {
		p.subsystem = sub
	}
}

func prometheusHandler() fasthttp.RequestHandler {
	return fasthttpadaptor.NewFastHTTPHandler(promhttp.Handler())
}

func (p *Prometheus) WrapHandler(r *fasthttprouter.Router) fasthttp.RequestHandler {

	// Setting prometheus metrics handler
	r.GET(p.MetricsPath, prometheusHandler())

	return func(ctx *fasthttp.RequestCtx) {
		p.reqConcurrent.Inc()
		defer p.reqConcurrent.Dec()

		if string(ctx.Request.URI().Path()) == defaultMetricPath {
			r.Handler(ctx)
			return
		}

		reqSize := make(chan int)
		frc := acquireRequestFromPool()
		ctx.Request.CopyTo(frc)
		go computeApproximateRequestSize(frc, reqSize)

		start := time.Now()
		r.Handler(ctx)

		status := strconv.Itoa(ctx.Response.StatusCode())
		elapsed := float64(time.Since(start)) / float64(time.Second)
		respSize := float64(len(ctx.Response.Body()))

		method := string(ctx.Method())
		endpoint := string(ctx.Request.URI().Path())

		p.reqDur.WithLabelValues(status, method, endpoint).Observe(elapsed)
		p.reqCnt.WithLabelValues(status, method, endpoint).Inc()
		p.reqSize.Observe(float64(<-reqSize))
		p.respSize.Observe(respSize)
	}
}

// Idea is from https://github.com/DanielHeckrath/gin-prometheus/blob/master/gin_prometheus.go and https://github.com/zsais/go-gin-prometheus/blob/master/middleware.go
func computeApproximateRequestSize(ctx *fasthttp.Request, out chan int) {
	s := 0
	if ctx.URI() != nil {
		s += len(ctx.URI().Path())
		s += len(ctx.URI().Host())
	}

	s += len(ctx.Header.Method())
	s += len("HTTP/1.1")

	ctx.Header.VisitAll(func(key, value []byte) {
		if string(key) != "Host" {
			s += len(key) + len(value)
		}
	})

	if ctx.Header.ContentLength() != -1 {
		s += ctx.Header.ContentLength()
	}

	out <- s
}

func (p *Prometheus) registerMetrics() {

	RequestDurationBucket := []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 15, 20, 30, 40, 50, 60}

	p.reqCnt = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: p.subsystem,
			Name:      "requests_total",
			Help:      "The HTTP request counts processed.",
		},
		[]string{"code", "method", "endpoint"},
	)

	p.reqDur = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: p.subsystem,
			Name:      "request_duration_seconds",
			Help:      "The HTTP request duration in seconds.",
			Buckets:   RequestDurationBucket,
		},
		[]string{"code", "method", "endpoint"},
	)

	p.reqSize = prometheus.NewSummary(
		prometheus.SummaryOpts{
			Subsystem: p.subsystem,
			Name:      "request_size_bytes",
			Help:      "The HTTP request sizes in bytes.",
		},
	)

	p.respSize = prometheus.NewSummary(
		prometheus.SummaryOpts{
			Subsystem: p.subsystem,
			Name:      "response_size_bytes",
			Help:      "The HTTP response sizes in bytes.",
		},
	)

	p.reqConcurrent = prometheus.NewGauge(prometheus.GaugeOpts{
		Subsystem: p.subsystem,
		Name:      "concurrent_requests",
		Help:      "Number of concurrent HTTP requests",
	},
	)

	collectors := []prometheus.Collector{
		p.reqConcurrent,
		p.reqCnt,
		p.reqDur,
		p.reqSize,
		p.respSize,
	}

	if p.registry != nil {
		p.registry.MustRegister(collectors...)
	} else {
		prometheus.MustRegister(collectors...)
	}
}

func acquireRequestFromPool() *fasthttp.Request {
	rp := requestHandlerPool.Get()

	if rp == nil {
		return new(fasthttp.Request)
	}

	frc := rp.(*fasthttp.Request)
	return frc
}
