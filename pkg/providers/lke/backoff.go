/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package lke

import (
	"fmt"
	"time"

	"github.com/patrickmn/go-cache"
)

type backoffEntry struct {
	failures int
	delay    time.Duration
	retryAt  time.Time
	err      error
}

// backoffTracker tracks consecutive failures per key and rejects further attempts until an
// exponentially growing delay has elapsed, so a persistently failing API call isn't hammered.
type backoffTracker struct {
	entries *cache.Cache
	base    time.Duration
	max     time.Duration
}

func newBackoffTracker(base, max time.Duration) *backoffTracker {
	return &backoffTracker{entries: cache.New(time.Hour, 10*time.Minute), base: base, max: max}
}

// check returns an error wrapping the last failure while key is backing off, or nil when an attempt is allowed.
func (b *backoffTracker) check(key, what string) error {
	v, ok := b.entries.Get(key)
	if !ok {
		return nil
	}
	entry := v.(backoffEntry)
	remaining := time.Until(entry.retryAt)
	if remaining <= 0 {
		return nil
	}
	return fmt.Errorf("%s is backing off for %s after %d failed attempts: %w", what, remaining.Round(time.Second), entry.failures, entry.err)
}

func (b *backoffTracker) recordFailure(key string, err error) {
	entry := backoffEntry{failures: 1, delay: b.base, err: err}
	if v, ok := b.entries.Get(key); ok {
		prev := v.(backoffEntry)
		entry.failures = prev.failures + 1
		entry.delay = min(prev.delay*2, b.max)
	}
	entry.retryAt = time.Now().Add(entry.delay)
	b.entries.SetDefault(key, entry)
}

func (b *backoffTracker) clear(key string) {
	b.entries.Delete(key)
}
