package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/karimra/gnmic/outputs"
	"github.com/openconfig/gnmi/proto/gnmi"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

// Config is the collector config
type Config struct {
	PrometheusAddress string
	Debug             bool
}

// Collector //
type Collector struct {
	Config        *Config
	Subscriptions map[string]*SubscriptionConfig
	Outputs       map[string][]outputs.Output
	DialOpts      []grpc.DialOption
	//
	m             *sync.Mutex
	Targets       map[string]*Target
	Logger        *log.Logger
	httpServer    *http.Server
	defaultOutput io.Writer
	ctx           context.Context
	cancelFn      context.CancelFunc
}

// NewCollector //
func NewCollector(ctx context.Context,
	config *Config,
	targetConfigs map[string]*TargetConfig,
	subscriptions map[string]*SubscriptionConfig,
	outputs map[string][]outputs.Output,
	dialOpts []grpc.DialOption,
	logger *log.Logger,
) *Collector {
	nctx, cancel := context.WithCancel(ctx)
	grpcMetrics := grpc_prometheus.NewClientMetrics()
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGoCollector())
	grpcMetrics.EnableClientHandlingTimeHistogram()
	reg.MustRegister(grpcMetrics)
	handler := http.NewServeMux()
	handler.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	httpServer := &http.Server{
		Handler: handler,
		Addr:    config.PrometheusAddress,
	}
	dialOpts = append(dialOpts, grpc.WithStreamInterceptor(grpcMetrics.StreamClientInterceptor()))
	c := &Collector{
		Config:        config,
		Subscriptions: subscriptions,
		Outputs:       outputs,
		DialOpts:      dialOpts,
		m:             new(sync.Mutex),
		Targets:       make(map[string]*Target),
		Logger:        logger,
		httpServer:    httpServer,
		defaultOutput: os.Stdout,
		ctx:           nctx,
		cancelFn:      cancel,
	}
	wg := new(sync.WaitGroup)
	wg.Add(len(targetConfigs))
	for _, tc := range targetConfigs {
		go func(tc *TargetConfig) {
			defer wg.Done()
			err := c.InitTarget(tc)
			if err == context.DeadlineExceeded {
				c.Logger.Printf("failed to initialize target '%s' timeout (%s) reached: %v", tc.Name, tc.Timeout, err)
				return
			}
			if err != nil {
				c.Logger.Printf("failed to initialize target '%s': %v", tc.Name, err)
				return
			}
			c.Logger.Printf("target '%s' initialized", tc.Name)
		}(tc)
	}
	wg.Wait()
	return c
}

// InitTarget initializes a target based on *TargetConfig
func (c *Collector) InitTarget(tc *TargetConfig) error {
	t := NewTarget(tc)
	//
	t.Subscriptions = make([]*SubscriptionConfig, 0, len(tc.Subscriptions))
	for _, subName := range tc.Subscriptions {
		if sub, ok := c.Subscriptions[subName]; ok {
			t.Subscriptions = append(t.Subscriptions, sub)
		}
	}
	if len(t.Subscriptions) == 0 {
		t.Subscriptions = make([]*SubscriptionConfig, 0, len(c.Subscriptions))
		for _, sub := range c.Subscriptions {
			t.Subscriptions = append(t.Subscriptions, sub)
		}
	}
	//
	t.Outputs = make([]outputs.Output, 0, len(tc.Outputs))
	for _, outName := range tc.Outputs {
		if outs, ok := c.Outputs[outName]; ok {
			t.Outputs = append(t.Outputs, outs...)
		}
	}
	if len(t.Outputs) == 0 {
		t.Outputs = make([]outputs.Output, 0, len(c.Outputs))
		for _, o := range c.Outputs {
			t.Outputs = append(t.Outputs, o...)
		}
	}
	//
	err := t.CreateGNMIClient(c.ctx, c.DialOpts...)
	if err != nil {
		return err
	}
	t.ctx, t.cancelFn = context.WithCancel(c.ctx)
	c.m.Lock()
	defer c.m.Unlock()
	c.Targets[t.Config.Name] = t
	return nil
}

// Subscribe //
func (c *Collector) Subscribe(tName string) error {
	if t, ok := c.Targets[tName]; ok {
		for _, sc := range t.Subscriptions {
			req, err := sc.CreateSubscribeRequest()
			if err != nil {
				return err
			}
			go t.Subscribe(c.ctx, req, sc.Name)
		}
		return nil
	}
	return fmt.Errorf("unknown target name: %s", tName)
}

// Start start the prometheus server as well as a goroutine per target selecting on the response chan, the error chan and the ctx.Done() chan
func (c *Collector) Start() {
	go func() {
		if err := c.httpServer.ListenAndServe(); err != nil {
			c.Logger.Printf("Unable to start prometheus http server: %v", err)
			return
		}
	}()
	wg := new(sync.WaitGroup)
	wg.Add(len(c.Targets))
	for _, t := range c.Targets {
		go func(t *Target) {
			defer wg.Done()
			numOnceSubscriptions := t.numberOfOnceSubscriptions()
			remainingOnceSubscriptions := numOnceSubscriptions
			numSubscriptions := len(t.Subscriptions)
			for {
				select {
				case rsp := <-t.SubscribeResponses:
					m := make(map[string]interface{})
					m["subscription-name"] = rsp.SubscriptionName
					m["source"] = t.Config.Name
					b, err := c.FormatMsg(m, rsp.Response)
					if err != nil {
						c.Logger.Printf("failed formatting msg from target '%s': %v", t.Config.Name, err)
						continue
					}
					go t.Export(b, outputs.Meta{"source": t.Config.Name})
					if remainingOnceSubscriptions > 0 {
						if c.subscriptionMode(rsp.SubscriptionName) == "ONCE" {
							switch rsp.Response.Response.(type) {
							case *gnmi.SubscribeResponse_SyncResponse:
								remainingOnceSubscriptions--
							}
						}
					}
					if remainingOnceSubscriptions == 0 && numSubscriptions == numOnceSubscriptions {
						return
					}
				case err := <-t.Errors:
					if err == io.EOF {
						c.Logger.Printf("target '%s' closed stream(EOF)", t.Config.Name)
						return
					}
					c.Logger.Printf("target '%s' error: %v", t.Config.Name, err)
				case <-t.ctx.Done():
					return
				}
			}
		}(t)
	}
	wg.Wait()
}

// FormatMsg formats the gnmi.SubscribeResponse and returns a []byte and an error
func (c *Collector) FormatMsg(meta map[string]interface{}, rsp *gnmi.SubscribeResponse) ([]byte, error) {
	if rsp == nil {
		return nil, nil
	}
	switch rsp := rsp.Response.(type) {
	case *gnmi.SubscribeResponse_Update:
		msg := new(msg)
		msg.Timestamp = rsp.Update.Timestamp
		t := time.Unix(0, rsp.Update.Timestamp)
		msg.Time = &t
		if meta == nil {
			meta = make(map[string]interface{})
		}
		msg.Prefix = gnmiPathToXPath(rsp.Update.Prefix)
		var ok bool
		if _, ok = meta["source"]; ok {
			msg.Source = fmt.Sprintf("%s", meta["source"])
		}
		if _, ok = meta["system-name"]; ok {
			msg.SystemName = fmt.Sprintf("%s", meta["system-name"])
		}
		if _, ok = meta["subscription-name"]; ok {
			msg.SubscriptionName = fmt.Sprintf("%s", meta["subscription-name"])
		}
		for i, upd := range rsp.Update.Update {
			pathElems := make([]string, 0, len(upd.Path.Elem))
			for _, pElem := range upd.Path.Elem {
				pathElems = append(pathElems, pElem.GetName())
			}
			value, err := getValue(upd.Val)
			if err != nil {
				c.Logger.Println(err)
			}
			msg.Updates = append(msg.Updates,
				&update{
					Path:   gnmiPathToXPath(upd.Path),
					Values: make(map[string]interface{}),
				})
			msg.Updates[i].Values[strings.Join(pathElems, "/")] = value
		}
		for _, del := range rsp.Update.Delete {
			msg.Deletes = append(msg.Deletes, gnmiPathToXPath(del))
		}
		data, err := json.Marshal(msg)
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, nil
}

// TargetPoll sends a gnmi.SubscribeRequest_Poll to targetName and returns the response and an error,
// it uses the targetName and the subscriptionName strings to find the gnmi.GNMI_SubscribeClient
func (c *Collector) TargetPoll(targetName, subscriptionName string) (*gnmi.SubscribeResponse, error) {
	if sub, ok := c.Subscriptions[subscriptionName]; ok {
		if strings.ToUpper(sub.Mode) != "POLL" {
			return nil, fmt.Errorf("subscription '%s' is not a POLL subscription", subscriptionName)
		}
		if t, ok := c.Targets[targetName]; ok {
			if subClient, ok := t.SubscribeClients[subscriptionName]; ok {
				err := subClient.Send(&gnmi.SubscribeRequest{
					Request: &gnmi.SubscribeRequest_Poll{
						Poll: &gnmi.Poll{},
					},
				})
				if err != nil {
					return nil, err
				}
				return subClient.Recv()
			}
		}
		return nil, fmt.Errorf("unknown target name '%s'", targetName)
	}
	return nil, fmt.Errorf("unknown subscription name '%s'", subscriptionName)
}

// PolledSubscriptionsTargets returns a map of target name to a list of subscription names that have Mode == POLL
func (c *Collector) PolledSubscriptionsTargets() map[string][]string {
	result := make(map[string][]string)
	for tn, target := range c.Targets {
		for _, sub := range target.Subscriptions {
			if strings.ToUpper(sub.Mode) == "POLL" {
				if result[tn] == nil {
					result[tn] = make([]string, 0)
				}
				result[tn] = append(result[tn], sub.Name)
			}
		}
	}
	return result
}

func (c *Collector) subscriptionMode(name string) string {
	if sub, ok := c.Subscriptions[name]; ok {
		return strings.ToUpper(sub.Mode)
	}
	return ""
}