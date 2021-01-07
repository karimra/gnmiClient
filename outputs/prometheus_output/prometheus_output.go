package prometheus_output

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/karimra/gnmic/formatters"
	"github.com/karimra/gnmic/outputs"
	"github.com/openconfig/gnmi/proto/gnmi"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/proto"
)

const (
	defaultListen     = ":9804"
	defaultPath       = "/metrics"
	defaultExpiration = time.Minute
	defaultMetricHelp = "gNMIc generated metric"
	metricNameRegex   = "[^a-zA-Z0-9_]+"
)

type labelPair struct {
	Name  string
	Value string
}
type promMetric struct {
	name   string
	labels []*labelPair
	time   time.Time
	value  float64
}

func init() {
	outputs.Register("prometheus", func() outputs.Output {
		return &PrometheusOutput{
			Cfg:         &Config{},
			eventChan:   make(chan *formatters.EventMsg),
			wg:          new(sync.WaitGroup),
			entries:     make(map[uint64]*promMetric),
			metricRegex: regexp.MustCompile(metricNameRegex),
		}
	})
}

type PrometheusOutput struct {
	Cfg       *Config
	metrics   []prometheus.Collector
	logger    *log.Logger
	eventChan chan *formatters.EventMsg

	wg     *sync.WaitGroup
	server *http.Server
	sync.Mutex
	entries map[uint64]*promMetric

	metricRegex *regexp.Regexp
	evps        []formatters.EventProcessor
}
type Config struct {
	Listen                 string        `mapstructure:"listen,omitempty"`
	Path                   string        `mapstructure:"path,omitempty"`
	Expiration             time.Duration `mapstructure:"expiration,omitempty"`
	MetricPrefix           string        `mapstructure:"metric-prefix,omitempty"`
	AppendSubscriptionName bool          `mapstructure:"append-subscription-name,omitempty"`
	Debug                  bool          `mapstructure:"debug,omitempty"`
	EventProcessors        []string      `mapstructure:"event-processors,omitempty"`
}

func (p *PrometheusOutput) String() string {
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(b)
}
func (p *PrometheusOutput) SetLogger(logger *log.Logger) {
	if logger != nil {
		p.logger = log.New(logger.Writer(), "prometheus_output ", logger.Flags())
		return
	}
	p.logger = log.New(os.Stderr, "prometheus_output ", log.LstdFlags|log.Lmicroseconds)
}

func (p *PrometheusOutput) SetEventProcessors(ps map[string]map[string]interface{}, log *log.Logger) {
	for _, epName := range p.Cfg.EventProcessors {
		if epCfg, ok := ps[epName]; ok {
			epType := ""
			for k := range epCfg {
				epType = k
				break
			}
			if in, ok := formatters.EventProcessors[epType]; ok {
				ep := in()
				err := ep.Init(epCfg[epType], log)
				if err != nil {
					p.logger.Printf("failed initializing event processor '%s' of type='%s': %v", epName, epType, err)
					continue
				}
				p.evps = append(p.evps, ep)
				p.logger.Printf("added event processor '%s' of type=%s to prometheus output", epName, epType)
			}
		}
	}
}

func (p *PrometheusOutput) Init(ctx context.Context, cfg map[string]interface{}, opts ...outputs.Option) error {
	for _, opt := range opts {
		opt(p)
	}
	err := outputs.DecodeConfig(cfg, p.Cfg)
	if err != nil {
		p.logger.Printf("prometheus output config decode failed: %v", err)
		return err
	}
	if p.Cfg.Listen == "" {
		p.Cfg.Listen = defaultListen
	}
	if p.Cfg.Path == "" {
		p.Cfg.Path = defaultPath
	}
	if p.Cfg.Expiration == 0 {
		p.Cfg.Expiration = defaultExpiration
	}

	// create prometheus registery
	registry := prometheus.NewRegistry()

	err = registry.Register(p)
	if err != nil {
		return err
	}
	// create http server
	promHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})

	mux := http.NewServeMux()
	mux.Handle(p.Cfg.Path, promHandler)

	p.server = &http.Server{
		Addr:    p.Cfg.Listen,
		Handler: mux,
	}

	// create tcp listener
	listener, err := net.Listen("tcp", p.Cfg.Listen)
	if err != nil {
		return err
	}
	// start worker
	p.wg.Add(2)
	wctx, wcancel := context.WithCancel(ctx)
	go p.worker(wctx)
	go func() {
		defer p.wg.Done()
		err = p.server.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			p.logger.Printf("prometheus server error: %v", err)
		}
		wcancel()
	}()
	p.logger.Printf("initialized prometheus output: %s", p.String())
	go func() {
		<-ctx.Done()
		p.Close()
	}()
	return nil
}

func (p *PrometheusOutput) Write(ctx context.Context, rsp proto.Message, meta outputs.Meta) {
	if rsp == nil {
		return
	}
	switch rsp := rsp.(type) {
	case *gnmi.SubscribeResponse:
		measName := "default"
		if subName, ok := meta["subscription-name"]; ok {
			measName = subName
		}
		events, err := formatters.ResponseToEventMsgs(measName, rsp, meta, p.evps...)
		if err != nil {
			p.logger.Printf("failed to convert message to event: %v", err)
			return
		}
		for _, ev := range events {
			select {
			case <-ctx.Done():
				return
			case p.eventChan <- ev:
			}
		}
	}
}

func (p *PrometheusOutput) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p.server.Shutdown(ctx)
	if err != nil {
		p.logger.Printf("failed to shutdown http server: %v", err)
	}
	p.logger.Printf("closed.")
	p.wg.Wait()
	return nil
}

func (p *PrometheusOutput) Metrics() []prometheus.Collector { return p.metrics }

///
func (p *PrometheusOutput) Describe(ch chan<- *prometheus.Desc) {}

func (p *PrometheusOutput) Collect(ch chan<- prometheus.Metric) {
	p.Lock()
	defer p.Unlock()

	for _, entry := range p.entries {
		ch <- entry
	}
}

func (p *PrometheusOutput) getLabels(ev *formatters.EventMsg) []*labelPair {
	labels := make([]*labelPair, 0, len(ev.Tags))
	addedLabels := make(map[string]struct{})
	for k, v := range ev.Tags {
		labelName := p.metricRegex.ReplaceAllString(filepath.Base(k), "_")
		if _, ok := addedLabels[labelName]; ok {
			continue
		}
		labels = append(labels, &labelPair{Name: labelName, Value: v})
		addedLabels[labelName] = struct{}{}
	}
	return labels
}

func (p *PrometheusOutput) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-p.eventChan:
			if p.Cfg.Debug {
				p.logger.Printf("got event to store: %+v", ev)
			}
			p.Lock()
			labels := p.getLabels(ev)
			for vName, val := range ev.Values {
				v, err := getFloat(val)
				if err != nil {
					continue
				}
				pm := &promMetric{
					name:   p.metricName(ev.Name, vName),
					labels: labels,
					time:   time.Unix(0, ev.Timestamp),
					value:  v,
				}
				key := pm.calculateKey()
				if e, ok := p.entries[key]; ok {
					if e.time.Before(pm.time) {
						p.entries[key] = pm
					}
				} else {
					p.entries[key] = pm
				}
				if p.Cfg.Debug {
					p.logger.Printf("saved key=%d, metric: %+v", key, pm)
				}
			}
			// expire entries
			expiry := time.Now().Add(-p.Cfg.Expiration)
			for k, e := range p.entries {
				if e.time.Before(expiry) {
					delete(p.entries, k)
				}
			}
			p.Unlock()
		}
	}
}

// Metric
func (p *promMetric) calculateKey() uint64 {
	h := fnv.New64a()
	h.Write([]byte(p.name))
	if len(p.labels) > 0 {
		h.Write([]byte(":"))
		sort.Slice(p.labels, func(i, j int) bool {
			return p.labels[i].Name < p.labels[j].Name
		})
		for _, label := range p.labels {
			h.Write([]byte(label.Name))
			h.Write([]byte(":"))
			h.Write([]byte(label.Value))
			h.Write([]byte(":"))
		}
	}
	return h.Sum64()
}
func (p *promMetric) String() string {
	if p == nil {
		return ""
	}
	sb := strings.Builder{}
	sb.WriteString("name=")
	sb.WriteString(p.name)
	sb.WriteString(",")
	numLabels := len(p.labels)
	if numLabels > 0 {
		sb.WriteString("labels=[")
		for i, lb := range p.labels {
			sb.WriteString(lb.Name)
			sb.WriteString("=")
			sb.WriteString(lb.Value)
			if i < numLabels-1 {
				sb.WriteString(",")
			}
		}
		sb.WriteString("],")
	}
	sb.WriteString(fmt.Sprintf("value=%f,", p.value))
	sb.WriteString("time=")
	sb.WriteString(p.time.String())
	return sb.String()
}
func (p *promMetric) Desc() *prometheus.Desc {
	labelNames := make([]string, 0, len(p.labels))
	for _, label := range p.labels {
		labelNames = append(labelNames, label.Name)
	}

	return prometheus.NewDesc(p.name, defaultMetricHelp, labelNames, nil)
}

func (p *promMetric) Write(out *dto.Metric) error {
	out.Untyped = &dto.Untyped{
		Value: &p.value,
	}
	out.Label = make([]*dto.LabelPair, 0, len(p.labels))
	for _, lb := range p.labels {
		out.Label = append(out.Label, &dto.LabelPair{Name: &lb.Name, Value: &lb.Value})
	}
	timestamp := p.time.UnixNano() / 1000000
	out.TimestampMs = &timestamp
	return nil
}

func getFloat(v interface{}) (float64, error) {
	switch i := v.(type) {
	case float64:
		return float64(i), nil
	case float32:
		return float64(i), nil
	case int64:
		return float64(i), nil
	case int32:
		return float64(i), nil
	case int16:
		return float64(i), nil
	case int8:
		return float64(i), nil
	case uint64:
		return float64(i), nil
	case uint32:
		return float64(i), nil
	case uint16:
		return float64(i), nil
	case uint8:
		return float64(i), nil
	case int:
		return float64(i), nil
	case uint:
		return float64(i), nil
	case string:
		f, err := strconv.ParseFloat(i, 64)
		if err != nil {
			return math.NaN(), err
		}
		return f, err
	default:
		return math.NaN(), errors.New("getFloat: unknown value is of incompatible type")
	}
}

func (p *PrometheusOutput) metricName(measName, valueName string) string {
	sb := strings.Builder{}
	n, _ := sb.WriteString(p.metricRegex.ReplaceAllString(p.Cfg.MetricPrefix, "_"))
	if n > 0 {
		sb.WriteString("_")
	}
	if p.Cfg.AppendSubscriptionName {
		sb.WriteString(strings.TrimRight(p.metricRegex.ReplaceAllString(measName, "_"), "_"))
		sb.WriteString("_")
	}
	sb.WriteString(strings.TrimLeft(p.metricRegex.ReplaceAllString(valueName, "_"), "_"))
	return sb.String()
}
