// +build zookeeper

package client

import (
	"strings"
	"sync"
	"time"

	"github.com/caser789/rpcj/log"
	"github.com/docker/libkv"
	"github.com/docker/libkv/store"
	"github.com/docker/libkv/store/zookeeper"
)

func init() {
	zookeeper.Register()
}

// ZookeeperDiscovery is a zoopkeer service discovery.
// It always returns the registered servers in zookeeper.
type ZookeeperDiscovery struct {
	basePath string
	kv       store.Store
	pairs    []*KVPair
	chans    []chan []*KVPair
	mu       sync.Mutex

	// -1 means it always retry to watch until zookeeper is ok, 0 means no retry.
	RetriesAfterWatchFailed int

	stopCh chan struct{}
}

// NewZookeeperDiscovery returns a new ZookeeperDiscovery.
func NewZookeeperDiscovery(basePath string, servicePath string, zkAddr []string, options *store.Config) ServiceDiscovery {
	if basePath[0] == '/' {
		basePath = basePath[1:]
	}

	if len(basePath) > 1 && strings.HasSuffix(basePath, "/") {
		basePath = basePath[:len(basePath)-1]
	}

	kv, err := libkv.NewStore(store.ZK, zkAddr, options)
	if err != nil {
		log.Infof("cannot create store: %v", err)
		panic(err)
	}

	return NewZookeeperDiscoveryWithStore(basePath+"/"+servicePath, kv)
}

// NewZookeeperDiscoveryWithStore returns a new ZookeeperDiscovery with specified store.
func NewZookeeperDiscoveryWithStore(basePath string, kv store.Store) ServiceDiscovery {
	if basePath[0] == '/' {
		basePath = basePath[1:]
	}
	d := &ZookeeperDiscovery{basePath: basePath, kv: kv}
	d.stopCh = make(chan struct{})

	ps, err := kv.List(basePath)
	if err != nil {
		log.Infof("cannot get services of from registry: %v", basePath, err)
		panic(err)
	}

	var pairs = make([]*KVPair, 0, len(ps))
	for _, p := range ps {
		pairs = append(pairs, &KVPair{Key: p.Key, Value: string(p.Value)})
	}
	d.pairs = pairs
	d.RetriesAfterWatchFailed = -1
	go d.watch()

	return d
}

// Clone clones this ServiceDiscovery with new servicePath.
func (d ZookeeperDiscovery) Clone(servicePath string) ServiceDiscovery {
	return NewZookeeperDiscoveryWithStore(d.basePath+"/"+servicePath, d.kv)
}

// GetServices returns the servers
func (d ZookeeperDiscovery) GetServices() []*KVPair {
	return d.pairs
}

// WatchService returns a nil chan.
func (d *ZookeeperDiscovery) WatchService() chan []*KVPair {
	ch := make(chan []*KVPair, 10)
	d.chans = append(d.chans, ch)
	return ch
}

func (d *ZookeeperDiscovery) RemoveWatcher(ch chan []*KVPair) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var chans []chan []*KVPair
	for _, c := range d.chans {
		if c == ch {
			continue
		}

		chans = append(chans, c)
	}

	d.chans = chans
}

func (d *ZookeeperDiscovery) watch() {
	for {
		var err error
		var c <-chan []*store.KVPair
		var tempDelay time.Duration

		retry := d.RetriesAfterWatchFailed
		for d.RetriesAfterWatchFailed == -1 || retry > 0 {
			c, err = d.kv.WatchTree(d.basePath, nil)
			if err != nil {
				if d.RetriesAfterWatchFailed > 0 {
					retry--
				}
				if tempDelay == 0 {
					tempDelay = 1 * time.Second
				} else {
					tempDelay *= 2
				}
				if max := 30 * time.Second; tempDelay > max {
					tempDelay = max
				}
				log.Warnf("can not watchtree (with retry %d, sleep %v): %s: %v", retry, tempDelay, d.basePath, err)
				time.Sleep(tempDelay)
				continue
			}
			break
		}

		if err != nil {
			log.Errorf("can't watch %s: %v", d.basePath, err)
			return
		}

		if err != nil {
			log.Fatalf("can not watchtree: %s: %v", d.basePath, err)
		}

	readChanges:
		for {
			select {
			case <-d.stopCh:
				log.Info("discovery has been closed")
				return
			case ps := <-c:
				if ps == nil {
					break readChanges
				}
				var pairs []*KVPair // latest servers
				for _, p := range ps {
					pairs = append(pairs, &KVPair{Key: p.Key, Value: string(p.Value)})
				}
				d.pairs = pairs

				for _, ch := range d.chans {
					ch := ch
					go func() {
						defer func() {
							if r := recover(); r != nil {

							}
						}()
						select {
						case ch <- pairs:
						case <-time.After(time.Minute):
							log.Warn("chan is full and new change has ben dropped")
						}
					}()
				}
			}
		}

		log.Warn("chan is closed and will rewatch")
	}
}

func (d *ZookeeperDiscovery) Close() {
	close(d.stopCh)
}
