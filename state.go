package circuitbreaker

import (
	"github.com/benbjohnson/clock"
	"github.com/cenkalti/backoff/v5"
)

// each implementations of state represents State of circuit breaker.
//
// ref: https://docs.microsoft.com/en-us/azure/architecture/patterns/circuit-breaker
type state interface {
	State() State
	onEntry(cb *CircuitBreaker)
	onExit(cb *CircuitBreaker)
	ready(cb *CircuitBreaker) bool
	onSuccess(cb *CircuitBreaker)
	onFail(cb *CircuitBreaker)
}

// [Closed state]
//
//	/onEntry
//	   - Reset counters.
//	   - Start ticker.
//	/ready
//	   - returns true.
//	/onFail
//	   - update counters.
//	   - If threshold reached, change state to [Open]
//	/onTicker
//	   - reset counters.
//	/onExit
//	   - stop ticker.
type stateClosed struct {
	ticker *clock.Ticker
	done   chan struct{}
}

func (st *stateClosed) State() State { return StateClosed }
func (st *stateClosed) onEntry(cb *CircuitBreaker) {
	cb.cnt.resetFailures()
	cb.openBackOff.Reset()
	if cb.interval > 0 {
		st.ticker = cb.clock.Ticker(cb.interval)
		st.done = make(chan struct{})
		go func() {
			for {
				select {
				case <-st.ticker.C:
					st.onTicker(cb)
				case <-st.done:
					st.ticker.Stop()
					return
				}
			}
		}()
	}
}

func (st *stateClosed) onExit(_ *CircuitBreaker) {
	if st.done != nil {
		close(st.done)
	}
}

func (st *stateClosed) onTicker(cb *CircuitBreaker) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.cnt.reset()
}

func (st *stateClosed) ready(_ *CircuitBreaker) bool { return true }
func (st *stateClosed) onSuccess(_ *CircuitBreaker)  {}
func (st *stateClosed) onFail(cb *CircuitBreaker) {
	if cb.shouldTrip(&cb.cnt) {
		cb.setState(&stateOpen{})
	}
}

// [Open state]
//
//	/onEntry
//	   - Start timer.
//	/ready
//	   - Returns false.
//	/onTimer
//	  - Change state to [HalfOpen].
//	/onExit
//	  - Stop timer.
type stateOpen struct {
	timer *clock.Timer
}

func (st *stateOpen) State() State { return StateOpen }

// onEntry starts a timer that transitions to HalfOpen after the next backoff
// period. If the configured BackOff returns backoff.Stop, the timer is not
// started and the CircuitBreaker stays in Open until SetState or Reset is
// called explicitly.
func (st *stateOpen) onEntry(cb *CircuitBreaker) {
	timeout := cb.openBackOff.NextBackOff()
	if timeout != backoff.Stop {
		st.timer = cb.clock.AfterFunc(timeout, func() { st.onTimer(cb) })
	}
}

func (st *stateOpen) onTimer(cb *CircuitBreaker) { cb.setStateWithLock(&stateHalfOpen{}) }
func (st *stateOpen) onExit(_ *CircuitBreaker) {
	if st.timer != nil {
		st.timer.Stop()
	}
}
func (st *stateOpen) ready(_ *CircuitBreaker) bool { return false }
func (st *stateOpen) onSuccess(_ *CircuitBreaker)  {}
func (st *stateOpen) onFail(_ *CircuitBreaker)     {}

// [HalfOpen state]
//
//	/ready
//	   -> returns true
//	/onSuccess
//	   -> Increment Success counter.
//	   -> If threshold reached, change state to [Closed].
//	/onFail
//	   -> change state to [Open].
type stateHalfOpen struct{}

func (st *stateHalfOpen) State() State                 { return StateHalfOpen }
func (st *stateHalfOpen) onEntry(cb *CircuitBreaker)   { cb.cnt.resetSuccesses() }
func (st *stateHalfOpen) onExit(_ *CircuitBreaker)     {}
func (st *stateHalfOpen) ready(_ *CircuitBreaker) bool { return true }
func (st *stateHalfOpen) onSuccess(cb *CircuitBreaker) {
	if cb.cnt.Successes >= cb.halfOpenMaxSuccesses {
		cb.setState(&stateClosed{})
	}
}

func (st *stateHalfOpen) onFail(cb *CircuitBreaker) {
	cb.setState(&stateOpen{})
}
