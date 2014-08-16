package circuitbreaker

import (
	"fmt"
	"sync"
	"time"
)

var defaultStatsPrefixf = "circuit.%s"

type Statter interface {
	Counter(sampleRate float32, bucket string, n ...int)
	Timing(sampleRate float32, bucket string, d ...time.Duration)
	Gauge(sampleRate float32, bucket string, value ...string)
}

type PanelEvent struct {
	Name  string
	Event BreakerEvent
}

// Panel tracks a group of circuit breakers by name.
type Panel struct {
	Statter      Statter
	StatsPrefixf string

	circuits map[string]CircuitBreaker

	lastTripTimes  map[string]time.Time
	tripTimesLock  sync.RWMutex
	panelLock      sync.RWMutex
	eventReceivers []chan PanelEvent
}

func NewPanel() *Panel {
	return &Panel{
		circuits:      make(map[string]CircuitBreaker),
		Statter:       &noopStatter{},
		StatsPrefixf:  defaultStatsPrefixf,
		lastTripTimes: make(map[string]time.Time)}
}

// Add sets the name as a reference to the given circuit breaker.
func (p *Panel) Add(name string, cb CircuitBreaker) {
	p.panelLock.Lock()
	p.circuits[name] = cb
	p.panelLock.Unlock()

	events := cb.Subscribe()

	go func() {
		for {
			event := <-events
			for _, receiver := range p.eventReceivers {
				receiver <- PanelEvent{name, event}
			}
			switch event {
			case BreakerTripped:
				p.breakerTripped(name)
			case BreakerReset:
				p.breakerReset(name)
			case BreakerFail:
				p.breakerFail(name)
			case BreakerReady:
				p.breakerReady(name)
			}
		}
	}()
}

// Get retrieves a circuit breaker by name.  If no circuit breaker exists, it
// returns the NoOp one and sets ok to false.
func (p *Panel) Get(name string) (CircuitBreaker, bool) {
	p.panelLock.RLock()
	cb, ok := p.circuits[name]
	p.panelLock.RUnlock()

	if ok {
		return cb, ok
	}

	return NoOp(), ok
}

// Subscribe returns a channel of PanelEvents. Whenever a breaker changes state,
// the PanelEvent will be sent over the channel. See BreakerEvent for the types of events.
func (p *Panel) Subscribe() <-chan PanelEvent {
	eventReader := make(chan PanelEvent)
	output := make(chan PanelEvent, 100)

	go func() {
		for v := range eventReader {
			select {
			case output <- v:
			default:
				<-output
				output <- v
			}
		}
	}()
	p.eventReceivers = append(p.eventReceivers, eventReader)
	return output
}

func (p *Panel) breakerTripped(name string) {
	p.Statter.Counter(1.0, fmt.Sprintf(p.StatsPrefixf, name)+".tripped", 1)
	p.tripTimesLock.Lock()
	p.lastTripTimes[name] = time.Now()
	p.tripTimesLock.Unlock()
}

func (p *Panel) breakerReset(name string) {
	bucket := fmt.Sprintf(p.StatsPrefixf, name)

	p.Statter.Counter(1.0, bucket+".reset", 1)

	p.tripTimesLock.RLock()
	lastTrip := p.lastTripTimes[name]
	p.tripTimesLock.RUnlock()

	if !lastTrip.IsZero() {
		p.Statter.Timing(1.0, bucket+".trip-time", time.Since(lastTrip))
		p.tripTimesLock.Lock()
		p.lastTripTimes[name] = time.Time{}
		p.tripTimesLock.Unlock()
	}
}

func (p *Panel) breakerFail(name string) {
	p.Statter.Counter(1.0, fmt.Sprintf(p.StatsPrefixf, name)+".fail", 1)
}

func (p *Panel) breakerReady(name string) {
	p.Statter.Counter(1.0, fmt.Sprintf(p.StatsPrefixf, name)+".ready", 1)
}

type noopStatter struct {
}

func (*noopStatter) Counter(sampleRate float32, bucket string, n ...int)          {}
func (*noopStatter) Timing(sampleRate float32, bucket string, d ...time.Duration) {}
func (*noopStatter) Gauge(sampleRate float32, bucket string, value ...string)     {}
