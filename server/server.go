package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/caser789/rpcj/log"
	"github.com/caser789/rpcj/protocol"
	"github.com/caser789/rpcj/share"
)

// ErrServerClosed is returned by the Server's Serve, ListenAndServe after a call to Shutdown or Close.
var ErrServerClosed = errors.New("http: Server closed")

const (
	// ReaderBuffsize is used for bufio reader.
	ReaderBuffsize = 1024
	// WriterBuffsize is used for bufio writer.
	WriterBuffsize = 1024
)

// contextKey is a value for use with context.WithValue. It's used as
// a pointer so it fits in an interface{} without allocation.
type contextKey struct {
	name string
}

func (k *contextKey) String() string { return "rpcx context value " + k.name }

var (
	// RemoteConnContextKey is a context key. It can be used in
	// services with context.WithValue to access the connection arrived on.
	// The associated value will be of type net.Conn.
	RemoteConnContextKey = &contextKey{"remote-conn"}
)

// Server is rpcx server that use TCP or UDP.
type Server struct {
	ln           net.Listener
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	serviceMapMu sync.RWMutex
	serviceMap   map[string]*service

	mu         sync.RWMutex
	activeConn map[net.Conn]struct{}
	doneChan   chan struct{}

	inShutdown int32
	onShutdown []func()

	// BlockCrypt for kcp.BlockCrypt, QUICConfig for quic TlsConfig, etc.
	Options map[string]interface{}
	// // use for KCP
	// KCPConfig KCPConfig
	// // for QUIC
	// QUICConfig QUICConfig

	Plugins PluginContainer

	// AuthFunc can be used to auth.
	AuthFunc func(req *protocol.Message, token string) error
}

// NewServer returns a server.
func NewServer(options map[string]interface{}) *Server {
	return &Server{
		Plugins: &pluginContainer{},
	}
}

// // KCPConfig is config of KCP.
// type KCPConfig struct {
// 	BlockCrypt kcp.BlockCrypt
// }

// // QUICConfig is config of QUIC.
// type QUICConfig struct {
// 	TlsConfig *tls.Config
// }

// Address returns listened address.
func (s *Server) Address() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

func (s *Server) getDoneChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.doneChan == nil {
		s.doneChan = make(chan struct{})
	}
	return s.doneChan
}

// Serve starts and listens RPC requests.
// It is blocked until receiving connectings from clients.
func (s *Server) Serve(network, address string) (err error) {
	var ln net.Listener
	ln, err = s.makeListener(network, address)
	if err != nil {
		return
	}

	if network == "http" {
		s.serveByHTTP(ln, "")
		return nil
	}
	return s.serveListener(ln)
}

// serveListener accepts incoming connections on the Listener ln,
// creating a new service goroutine for each.
// The service goroutines read requests and then call services to reply to them.
func (s *Server) serveListener(ln net.Listener) error {
	s.ln = ln

	if s.Plugins == nil {
		s.Plugins = &pluginContainer{}
	}

	var tempDelay time.Duration

	s.mu.Lock()
	if s.activeConn == nil {
		s.activeConn = make(map[net.Conn]struct{})
	}
	s.mu.Unlock()

	for {
		conn, e := ln.Accept()
		if e != nil {
			select {
			case <-s.getDoneChan():
				return ErrServerClosed
			default:
			}

			if ne, ok := e.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}

				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}

				log.Errorf("rpcx: Accept error: %v; retrying in %v", e, tempDelay)
				time.Sleep(tempDelay)
				continue
			}
			return e
		}
		tempDelay = 0

		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(3 * time.Minute)
		}

		s.mu.Lock()
		s.activeConn[conn] = struct{}{}
		s.mu.Unlock()

		conn, ok := s.Plugins.DoPostConnAccept(conn)
		if !ok {
			continue
		}

		go s.serveConn(conn)
	}
}

// serveByHTTP serves by HTTP.
// if rpcPath is an empty string, use share.DefaultRPCPath.
func (s *Server) serveByHTTP(ln net.Listener, rpcPath string) {
	s.ln = ln

	if s.Plugins == nil {
		s.Plugins = &pluginContainer{}
	}

	if rpcPath == "" {
		rpcPath = share.DefaultRPCPath
	}
	http.Handle(rpcPath, s)
	srv := &http.Server{Handler: nil}

	s.mu.Lock()
	if s.activeConn == nil {
		s.activeConn = make(map[net.Conn]struct{})
	}
	s.mu.Unlock()

	srv.Serve(ln)
}

func (s *Server) serveConn(conn net.Conn) {
	defer func() {
		if err := recover(); err != nil {
			const size = 64 << 10
			buf := make([]byte, size)
			buf = buf[:runtime.Stack(buf, false)]
			log.Errorf("serving %s panic error: %s, stack:\n %s", conn.RemoteAddr(), err, buf)
		}
		s.mu.Lock()
		delete(s.activeConn, conn)
		s.mu.Unlock()
		conn.Close()
	}()

	if tlsConn, ok := conn.(*tls.Conn); ok {
		if d := s.ReadTimeout; d != 0 {
			conn.SetReadDeadline(time.Now().Add(d))
		}
		if d := s.WriteTimeout; d != 0 {
			conn.SetWriteDeadline(time.Now().Add(d))
		}
		if err := tlsConn.Handshake(); err != nil {
			log.Errorf("rpcx: TLS handshake error from %s: %v", conn.RemoteAddr(), err)
			return
		}
	}

	ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)
	r := bufio.NewReaderSize(conn, ReaderBuffsize)
	w := bufio.NewWriterSize(conn, WriterBuffsize)

	for {
		t0 := time.Now()
		if s.ReadTimeout != 0 {
			conn.SetReadDeadline(t0.Add(s.ReadTimeout))
		}

		req, err := s.readRequest(ctx, r)
		if err != nil {
			if err == io.EOF {
				log.Infof("client has closed this connection: %s", conn.RemoteAddr().String())
			} else {
				log.Errorf("rpcx: failed to read request: %v", err)
			}
			return
		}

		if s.WriteTimeout != 0 {
			conn.SetWriteDeadline(t0.Add(s.WriteTimeout))
		}

		go func() {
			res, err := s.handleRequest(ctx, req)
			if err != nil {
				log.Errorf("rpcx: failed to handle request: %v", err)
			}
			s.Plugins.DoPreWriteResponse(ctx, req)
			if !req.IsOneway() {
				res.WriteTo(w)
				w.Flush()
			}
			s.Plugins.DoPostWriteResponse(ctx, req, res, err)
		}()
	}
}

func (s *Server) readRequest(ctx context.Context, r io.Reader) (req *protocol.Message, err error) {
	s.Plugins.DoPreReadRequest(ctx)
	// pool req?
	req = protocol.NewMessage()
	err = req.Decode(r)
	s.Plugins.DoPostReadRequest(ctx, req, err)

	if s.AuthFunc != nil && err == nil {
		token := req.Metadata[share.AuthKey]
		err = s.AuthFunc(req, token)
	}

	return req, err
}

func (s *Server) handleRequest(ctx context.Context, req *protocol.Message) (res *protocol.Message, err error) {
	res = req.Clone()
	res.SetMessageType(protocol.Response)

	serviceName := req.Metadata[protocol.ServicePath]
	methodName := req.Metadata[protocol.ServiceMethod]

	s.serviceMapMu.RLock()
	service := s.serviceMap[serviceName]
	s.serviceMapMu.RUnlock()
	if service == nil {
		err = errors.New("rpcx: can't find service " + serviceName)
		return handleError(res, err)
	}
	mtype := service.method[methodName]
	if mtype == nil {
		err = errors.New("rpcx: can't find method " + methodName)
		return handleError(res, err)
	}

	var argv, replyv reflect.Value

	argIsValue := false // if true, need to indirect before calling.
	if mtype.ArgType.Kind() == reflect.Ptr {
		argv = reflect.New(mtype.ArgType.Elem())
	} else {
		argv = reflect.New(mtype.ArgType)
		argIsValue = true
	}

	if argIsValue {
		argv = argv.Elem()
	}

	codec := share.Codecs[req.SerializeType()]
	if codec == nil {
		err = fmt.Errorf("can not find codec for %d", req.SerializeType())
		return handleError(res, err)
	}

	err = codec.Decode(req.Payload, argv.Interface())
	if err != nil {
		return handleError(res, err)
	}

	replyv = reflect.New(mtype.ReplyType.Elem())

	err = service.call(ctx, mtype, argv, replyv)
	if err != nil {
		return handleError(res, err)
	}

	if !req.IsOneway() {
		data, err := codec.Encode(replyv.Interface())
		if err != nil {
			return handleError(res, err)
		}
		res.Payload = data
	}
	return res, nil
}

func handleError(res *protocol.Message, err error) (*protocol.Message, error) {
	res.SetMessageStatusType(protocol.Error)
	res.Metadata[protocol.ServiceError] = err.Error()
	return res, err
}

// Can connect to RPC service using HTTP CONNECT to rpcPath.
var connected = "200 Connected to rpcx"

// ServeHTTP implements an http.Handler that answers RPC requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		io.WriteString(w, "405 must CONNECT\n")
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Info("rpc hijacking ", req.RemoteAddr, ": ", err.Error())
		return
	}
	io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")

	s.mu.Lock()
	s.activeConn[conn] = struct{}{}
	s.mu.Unlock()

	s.serveConn(conn)
}

// Close immediately closes all active net.Listeners.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeDoneChanLocked()
	var err error
	if s.ln != nil {
		err = s.ln.Close()
	}

	for c := range s.activeConn {
		c.Close()
		delete(s.activeConn, c)
	}
	return err
}

// RegisterOnShutdown registers a function to call on Shutdown.
// This can be used to gracefully shutdown connections.
func (s *Server) RegisterOnShutdown(f func()) {
	s.mu.Lock()
	s.onShutdown = append(s.onShutdown, f)
	s.mu.Unlock()
}

// var shutdownPollInterval = 500 * time.Millisecond

// // Shutdown gracefully shuts down the server without interrupting any
// // active connections. Shutdown works by first closing the
// // listener, then closing all idle connections, and then waiting
// // indefinitely for connections to return to idle and then shut down.
// // If the provided context expires before the shutdown is complete,
// // Shutdown returns the context's error, otherwise it returns any
// // error returned from closing the Server's underlying Listener.
// func (s *Server) Shutdown(ctx context.Context) error {
// 	atomic.AddInt32(&s.inShutdown, 1)
// 	defer atomic.AddInt32(&s.inShutdown, -1)

// 	s.mu.Lock()
// 	err := s.ln.Close()
// 	s.closeDoneChanLocked()
// 	for _, f := range s.onShutdown {
// 		go f()
// 	}
// 	s.mu.Unlock()

// 	ticker := time.NewTicker(shutdownPollInterval)
// 	defer ticker.Stop()
// 	for {
// 		if s.closeIdleConns() {
// 			return err
// 		}
// 		select {
// 		case <-ctx.Done():
// 			return ctx.Err()
// 		case <-ticker.C:
// 		}
// 	}
// }

// func (s *Server) closeIdleConns() {
// 	s.mu.Lock()
// 	defer s.mu.Unlock()
// 	quiescent := true

// 	for c := range s.activeConn {
// 		// check whether the conn is idle
// 		st, ok := c.curState.Load().(ConnState)
// 		if !ok || st != StateIdle {
// 			quiescent = false
// 			continue
// 		}

// 		s.Close()
// 		delete(s.activeConn, c)
// 	}

// 	return quiescent
// }

func (s *Server) closeDoneChanLocked() {
	ch := s.getDoneChanLocked()
	select {
	case <-ch:
		// Already closed. Don't close again.
	default:
		// Safe to close here. We're the only closer, guarded
		// by s.mu.
		close(ch)
	}
}
func (s *Server) getDoneChanLocked() chan struct{} {
	if s.doneChan == nil {
		s.doneChan = make(chan struct{})
	}
	return s.doneChan
}

var ip4Reg = regexp.MustCompile(`^(([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])\.){3}([0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])$`)

func validIP4(ipAddress string) bool {
	ipAddress = strings.Trim(ipAddress, " ")
	i := strings.LastIndex(ipAddress, ":")
	ipAddress = ipAddress[:i] //remove port

	return ip4Reg.MatchString(ipAddress)
}
