package event_trigger

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"time"

	"github.com/antonmedv/expr"
	"github.com/antonmedv/expr/vm"
	"github.com/karimra/gnmic/actions"
	_ "github.com/karimra/gnmic/actions/all"
	"github.com/karimra/gnmic/formatters"
)

const (
	processorType    = "event-trigger"
	loggingPrefix    = "[" + processorType + "] "
	defaultCondition = "true"
)

// Trigger triggers an action when certain conditions are met
type Trigger struct {
	formatters.EventProcessor

	Condition      string                 `mapstructure:"condition,omitempty"`
	MaxOccurrences int                    `mapstructure:"max-occurrences,omitempty"`
	Window         time.Duration          `mapstructure:"window,omitempty"`
	Action         map[string]interface{} `mapstructure:"action,omitempty"`
	Debug          bool                   `mapstructure:"debug,omitempty"`

	//numOccurrences   int
	occurrencesTimes []time.Time
	prg              *vm.Program
	action           actions.Action

	targets map[string]interface{}
	logger  *log.Logger
}

func init() {
	formatters.Register(processorType, func() formatters.EventProcessor {
		return &Trigger{
			logger: log.New(ioutil.Discard, "", 0),
		}
	})
}

func (p *Trigger) Init(cfg interface{}, opts ...formatters.Option) error {
	err := formatters.DecodeConfig(cfg, p)
	if err != nil {
		return err
	}
	for _, opt := range opts {
		opt(p)
	}

	p.prg, err = expr.Compile(p.Condition)
	if err != nil {
		return err
	}
	err = p.initializeAction(p.Action)
	if err != nil {
		return err
	}
	err = p.setDefaults()
	if err != nil {
		return err
	}
	p.logger.Printf("%q initalized: %+v", processorType, p)
	return nil
}

func (p *Trigger) Apply(es ...*formatters.EventMsg) []*formatters.EventMsg {
	now := time.Now()
	for _, e := range es {
		if e == nil {
			continue
		}
		res, err := expr.Run(p.prg, e)
		if err != nil {
			p.logger.Printf("failed evaluating: %v", err)
			continue
		}
		p.logger.Printf("expression result: (%T)%+v", res, res)
		switch res := res.(type) {
		case bool:
			if res {
				if p.MaxOccurrences == 1 {
					p.triggerAction(e)
					continue
				}

				p.occurrencesTimes = append(p.occurrencesTimes, now)
				// remove times out of the window
				numTimes := len(p.occurrencesTimes)
				validTimes := make([]time.Time, 0, numTimes)
				for _, t := range p.occurrencesTimes {
					if t.Add(p.Window).Before(now) {
						validTimes = append(validTimes, t)
					}
				}
				p.occurrencesTimes = validTimes
				numTimes = len(p.occurrencesTimes)
				if numTimes < p.MaxOccurrences {
					// not enough occurrences
					continue
				}
				// enough occurrences
				// within the window
				// max occurrences reached
				// run the action
				p.triggerAction(e)
			}
		}
	}
	return nil
}

func (p *Trigger) WithLogger(l *log.Logger) {
	if p.Debug && l != nil {
		p.logger = log.New(l.Writer(), loggingPrefix, l.Flags())
	} else if p.Debug {
		p.logger = log.New(os.Stderr, loggingPrefix, log.LstdFlags|log.Lmicroseconds)
	}
}

func (p *Trigger) WithTargets(tcs map[string]interface{}) {
	p.targets = tcs
}

func (p *Trigger) initializeAction(cfg map[string]interface{}) error {
	if len(cfg) == 0 {
		return errors.New("missing action definition")
	}
	if actType, ok := cfg["type"]; ok {
		switch actType := actType.(type) {
		case string:
			if in, ok := actions.Actions[actType]; ok {
				p.action = in()
				err := p.action.Init(cfg, actions.WithLogger(p.logger))
				if err != nil {
					return err
				}
				return nil
			}
			return fmt.Errorf("unknown action type %q", actType)
		default:
			return fmt.Errorf("unexpected action field type %T", actType)
		}
	}
	return errors.New("missing type field under action")
}

func (p *Trigger) String() string {
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func (p *Trigger) setDefaults() error {
	if p.Condition == "" {
		p.Condition = defaultCondition
	}
	if p.MaxOccurrences <= 0 {
		p.MaxOccurrences = 1
	}
	if p.Window <= 0 && p.MaxOccurrences > 1 {
		p.Window = time.Minute
	}
	return nil
}

func (p *Trigger) triggerAction(e *formatters.EventMsg) {
	p.logger.Printf("running action: %+v", p.action)
	go func() {
		res, err := p.action.Run(e)
		if err != nil {
			p.logger.Printf("trigger action %+v failed: %+v", p.action, err)
			return
		}
		p.logger.Printf("result: %+v", res)
	}()
}