package password

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	encoded, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !Verify("correct horse battery staple", encoded) {
		t.Fatal("expected password to verify")
	}
	if Verify("wrong password", encoded) {
		t.Fatal("wrong password verified")
	}
	if Verify("anything", "not-a-hash") {
		t.Fatal("invalid hash verified")
	}
}

func TestDefaultLimiterCapacity(t *testing.T) {
	if got := defaultLimiter.Capacity(); got != maxConcurrent {
		t.Fatalf("default limiter capacity = %d, want %d", got, maxConcurrent)
	}
	if maxConcurrent != 2 {
		t.Fatalf("maxConcurrent = %d, want 2", maxConcurrent)
	}
}

func TestLimiterBoundsConcurrency(t *testing.T) {
	const concurrency = 2
	const workers = 8
	limiter := NewLimiter(concurrency)

	var active, peak atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			limiter.Do(func() {
				current := active.Add(1)
				for {
					observed := peak.Load()
					if current <= observed || peak.CompareAndSwap(observed, current) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				active.Add(-1)
			})
		}()
	}
	close(start)
	wg.Wait()

	if got := peak.Load(); got != concurrency {
		t.Fatalf("peak concurrency = %d, want %d (semaphore must bound but not serialize)", got, concurrency)
	}
}

func TestLimiterSerializesWhenCapacityOne(t *testing.T) {
	limiter := NewLimiter(0) // clamped to 1
	if got := limiter.Capacity(); got != 1 {
		t.Fatalf("capacity = %d, want 1", got)
	}
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limiter.Do(func() {
				current := active.Add(1)
				for {
					observed := peak.Load()
					if current <= observed || peak.CompareAndSwap(observed, current) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				active.Add(-1)
			})
		}()
	}
	wg.Wait()
	if got := peak.Load(); got != 1 {
		t.Fatalf("peak concurrency = %d, want 1", got)
	}
}

func TestDummyHashIsValidPHC(t *testing.T) {
	p, salt, key, err := parse(dummyHash)
	if err != nil {
		t.Fatalf("dummy hash must parse: %v", err)
	}
	if p.memory != memory || p.iterations != iterations || p.parallelism != parallelism {
		t.Fatalf("dummy hash params = %+v, want m=%d,t=%d,p=%d", p, memory, iterations, parallelism)
	}
	if len(salt) != saltLength || len(key) != keyLength {
		t.Fatalf("dummy hash salt/key lengths = %d/%d, want %d/%d", len(salt), len(key), saltLength, keyLength)
	}
	// Must perform real work and return false for arbitrary input.
	if Verify("not-the-dummy-password", dummyHash) {
		t.Fatal("dummy password unexpectedly verifies")
	}
	VerifyDummy("anything")
}
