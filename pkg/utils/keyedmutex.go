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

package utils

import "sync"

// KeyedMutex provides per-key synchronization using sync.Map + sync.Mutex.
// NOTE: Lock entries accumulate indefinitely in the sync.Map. This is acceptable
// because pool keys are bounded by (NodePool count × InstanceType count), which
// is typically < 20-40 entries. If this becomes an issue, we can consider
// adding periodic cleanup.
type KeyedMutex struct {
	mu    sync.Mutex
	locks sync.Map
}

func NewKeyedMutex() *KeyedMutex {
	return &KeyedMutex{}
}

func (km *KeyedMutex) Lock(key string) {
	km.mu.Lock()
	lockI, _ := km.locks.LoadOrStore(key, &sync.Mutex{})
	lock := lockI.(*sync.Mutex)
	km.mu.Unlock()
	lock.Lock()
}

func (km *KeyedMutex) Unlock(key string) {
	lockI, ok := km.locks.Load(key)
	if !ok {
		panic("keyedmutex: unlock of unlocked key: " + key)
	}
	lockI.(*sync.Mutex).Unlock()
}
