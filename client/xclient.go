package client

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	ex "github.com/caser789/rpcj/errors"
)

// TODO

var (
	// ErrXClientShutdown xclient is shutdown.
	ErrXClientShutdown = errors.New("xClient is shut down")
	// ErrXClientNoServer selector can't found one server.
	ErrXClientNoServer = errors.New("can not found any server")
)

// XClient is an interface that used by client with service discovery and service governance.
// One XClient is used only for one service. You should create multiple XClient for multiple services.
type XClient interface {
	Go(ctx context.Context, args interface{}, reply interface{}, done chan *Call) (*Call, error)
	Call(ctx context.Context, args interface{}, reply interface{}) error
	Broadcast(ctx context.Context, args interface{}, reply interface{}) error
	Fork(ctx context.Context, args interface{}, reply interface{}) error
	Close() error
}

// KVPair contains a key and a string.
type KVPair struct {
	Key   string
	Value string
}

// ServiceDiscovery defines ServiceDiscovery of zookeeper, etcd and consul
type ServiceDiscovery interface {
	GetServices() []*KVPair
	WatchService() chan []*KVPair
}

type xClient struct {
	Retries       int
	failMode      FailMode
	selectMode    SelectMode
	cachedClient  map[string]*Client
	servicePath   string
	serviceMethod string
	option        Option

	mu        sync.RWMutex
	servers   map[string]string
	discovery ServiceDiscovery
	selector  Selector

	isShutdown bool
}

// NewXClient creates a XClient that supports service discovery and service governance.
func NewXClient(servicePath, serviceMethod string, failMode FailMode, selectMode SelectMode, discovery ServiceDiscovery, option Option) XClient {
	client := &xClient{
		Retries:       3,
		failMode:      failMode,
		selectMode:    selectMode,
		discovery:     discovery,
		servicePath:   servicePath,
		serviceMethod: serviceMethod,
		cachedClient:  make(map[string]*Client),
		option:        option,
	}

	ch := client.discovery.WatchService()
	if ch != nil {
		go client.watch(ch)
	}

	servers := make(map[string]string)
	pairs := discovery.GetServices()
	for _, p := range pairs {
		servers[p.Key] = p.Value
	}
	client.servers = servers
	client.selector = newSelector(selectMode, servers)

	return client
}

// watch changes of service and update cached clients.
func (c *xClient) watch(ch chan []*KVPair) {
	for pairs := range ch {

		servers := make(map[string]string)
		for _, p := range pairs {
			servers[p.Key] = p.Value
		}
		c.mu.Lock()
		c.servers = servers
		// TODO update other fields
		c.mu.Unlock()
	}
}

// selects a client from candidates base on c.selectMode
func (c *xClient) selectClient(ctx context.Context, servicePath, serviceMethod string) (*Client, error) {
	k := c.selector.Select(ctx, servicePath, serviceMethod)
	if k == "" {
		return nil, ErrXClientNoServer
	}

	return c.getCachedClient(k)
}

func (c *xClient) getCachedClient(k string) (*Client, error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Recovered in f", r)
		}
	}()

	c.mu.RLock()
	client := c.cachedClient[k]
	if client != nil {
		if !client.closing && !client.shutdown {
			c.mu.RUnlock()
			return client, nil
		}
	}
	c.mu.RUnlock()

	//double check
	c.mu.Lock()
	client = c.cachedClient[k]
	if client == nil {
		network, addr := splitNetworkAndAddress(k)
		client = &Client{
			option: c.option,
		}
		err := client.Connect(network, addr)
		if err != nil {
			c.mu.Unlock()
			return nil, err
		}
		c.cachedClient[k] = client
	}
	c.mu.Unlock()

	return client, nil
}

func splitNetworkAndAddress(server string) (string, string) {
	ss := strings.SplitN(server, "@", 2)
	if len(ss) == 1 {
		return "tcp", server
	}

	return ss[0], ss[1]
}

// Go invokes the function asynchronously. It returns the Call structure representing the invocation. The done channel will signal when the call is complete by returning the same Call object. If done is nil, Go will allocate a new channel. If non-nil, done must be buffered or Go will deliberately crash.
// It does not use FailMode.
func (c *xClient) Go(ctx context.Context, args interface{}, reply interface{}, done chan *Call) (*Call, error) {
	if c.isShutdown {
		return nil, ErrXClientShutdown
	}
	client, err := c.selectClient(ctx, c.servicePath, c.serviceMethod)
	if err != nil {
		return nil, err
	}
	return client.Go(ctx, c.servicePath, c.serviceMethod, args, reply, done), nil
}

// Call invokes the named function, waits for it to complete, and returns its error status.
// It handles errors base on FailMode.
func (c *xClient) Call(ctx context.Context, args interface{}, reply interface{}) error {
	if c.isShutdown {
		return ErrXClientShutdown
	}

	var err error
	client, err := c.selectClient(ctx, c.servicePath, c.serviceMethod)
	if err != nil {
		return err
	}

	switch c.failMode {
	case Failtry:
		retries := c.Retries
		for retries > 0 {
			retries--
			err = client.call(ctx, c.servicePath, c.serviceMethod, args, reply)
		}
		return err
	case Failover:
		retries := c.Retries
		for retries > 0 {
			retries--
			err = client.call(ctx, c.servicePath, c.serviceMethod, args, reply)
			if err == nil {
				return nil
			}

			//select another server
			client, err = c.selectClient(ctx, c.servicePath, c.serviceMethod)
			if err != nil {
				return err
			}
		}
		return err

	default: //Failfast
		return client.call(ctx, c.servicePath, c.serviceMethod, args, reply)
	}
}

// Broadcast sends requests to all servers and Success only when all servers return OK.
// FailMode and SelectMode are meanless for this method.
// Please set timeout to avoid hanging.
func (c *xClient) Broadcast(ctx context.Context, args interface{}, reply interface{}) error {
	var clients []*Client
	c.mu.RLock()
	for k := range c.servers {
		client, err := c.getCachedClient(k)
		if err != nil {
			c.mu.RUnlock()
			return err
		}
		clients = append(clients, client)
	}
	c.mu.RUnlock()

	if len(clients) == 0 {
		return ErrXClientNoServer
	}

	var err error
	l := len(clients)
	done := make(chan bool, l)
	for _, client := range clients {
		client := client
		go func() {
			err = client.Call(ctx, c.servicePath, c.serviceMethod, args, reply)
			done <- (err == nil)
			return
		}()
	}

	timeout := time.After(time.Minute)
check:
	for {
		select {
		case result := <-done:
			l--
			if l == 0 || !result { // all returns or some one returns an error
				break check
			}
		case <-timeout:
			break check
		}
	}

	return err
}

// Fork sends requests to all servers and Success once one server returns OK.
// FailMode and SelectMode are meanless for this method.
func (c *xClient) Fork(ctx context.Context, args interface{}, reply interface{}) error {
	var clients []*Client
	c.mu.RLock()
	for k := range c.servers {
		client, err := c.getCachedClient(k)
		if err != nil {
			c.mu.RUnlock()
			return err
		}
		clients = append(clients, client)
	}
	c.mu.RUnlock()

	if len(clients) == 0 {
		return ErrXClientNoServer
	}

	var err error
	l := len(clients)
	done := make(chan bool, l)
	for _, client := range clients {
		client := client
		go func() {
			clonedReply := reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			err = client.Call(ctx, c.servicePath, c.serviceMethod, args, clonedReply)
			done <- (err == nil)
			if err == nil {
				reflect.ValueOf(reply).Set(reflect.ValueOf(reply))
			}
			return
		}()
	}

	timeout := time.After(time.Minute)
check:
	for {
		select {
		case result := <-done:
			l--
			if result {
				return nil
			}
			if l == 0 { // all returns or some one returns an error
				break check
			}

		case <-timeout:
			break check
		}
	}

	return err
}

// Close closes this client and its underlying connnections to services.
func (c *xClient) Close() error {
	c.isShutdown = true

	var errs []error
	c.mu.Lock()
	for _, v := range c.cachedClient {
		e := v.Close()
		if e != nil {
			errs = append(errs, e)
		}

	}
	c.mu.Unlock()

	if len(errs) > 0 {
		return ex.NewMultiError(errs)
	}
	return nil
}
