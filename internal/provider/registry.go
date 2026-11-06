// Package provider is the worker-side platform adapter skeleton. Only fixed
// base URLs are allowed; callers never supply arbitrary URLs. The fixture
// provider makes the orchestration runnable without real platform keys.
package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Offer is a normalized platform observation. Nil price/shipping means unknown.
type Offer struct {
	Provider    string
	Title       string
	PriceFen    *int64
	ShippingFen *int64
	URL         string
}

type Provider interface {
	Name() string
	Search(context.Context, string) ([]Offer, error)
}

type Result struct {
	Provider string
	Offers   []Offer
	Err      error
	Latency  time.Duration
}

// Registry fans out to providers with independent timeouts and a per-provider
// concurrency slot. It is a skeleton: retry/circuit breakers can be added
// without changing the worker contract.
type Registry struct {
	timeout   time.Duration
	semaphore chan struct{}
	providers []Provider
}

func NewRegistry(timeout time.Duration, providers ...Provider) *Registry {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return &Registry{timeout: timeout, semaphore: make(chan struct{}, 4), providers: providers}
}

func (r *Registry) Search(ctx context.Context, query string) []Result {
	results := make([]Result, len(r.providers))
	var wg sync.WaitGroup
	for index, provider := range r.providers {
		index, provider := index, provider
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			result := Result{Provider: provider.Name()}
			select {
			case r.semaphore <- struct{}{}:
				defer func() { <-r.semaphore }()
			case <-ctx.Done():
				result.Err = ctx.Err()
				result.Latency = time.Since(started)
				results[index] = result
				return
			}
			attemptCtx, cancel := context.WithTimeout(ctx, r.timeout)
			defer cancel()
			offers, err := provider.Search(attemptCtx, query)
			result.Offers = offers
			result.Err = err
			result.Latency = time.Since(started)
			results[index] = result
		}()
	}
	wg.Wait()
	return results
}

// FixtureProvider returns preconfigured offers and never performs I/O.
type FixtureProvider struct {
	ProviderName string
	Offers       []Offer
	Err          error
}

func (f FixtureProvider) Name() string {
	if f.ProviderName == "" {
		return "fixture"
	}
	return f.ProviderName
}

func (f FixtureProvider) Search(context.Context, string) ([]Offer, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return append([]Offer(nil), f.Offers...), nil
}

// Validate checks the fixed-URL and money invariants before an offer reaches
// business logic.
func Validate(offer Offer) error {
	if offer.Provider == "" {
		return errors.New("provider: empty provider name")
	}
	if offer.PriceFen != nil && *offer.PriceFen < 0 {
		return fmt.Errorf("provider: negative price from %s", offer.Provider)
	}
	if offer.ShippingFen != nil && *offer.ShippingFen < 0 {
		return fmt.Errorf("provider: negative shipping from %s", offer.Provider)
	}
	return nil
}
