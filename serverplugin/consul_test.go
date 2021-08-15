// +build consul

package serverplugin

import (
	"testing"
	"time"

	"github.com/caser789/rpcj/server"
	metrics "github.com/rcrowley/go-metrics"
)

func TestConsulRegistry(t *testing.T) {
	s := server.NewServer(nil)

	r := &ConsulRegisterPlugin{
		ServiceAddress: "tcp@127.0.0.1:8972",
		ConsulServers:  []string{"127.0.0.1:8500"},
		BasePath:       "/rpcx_test",
		Metrics:        metrics.NewRegistry(),
		Services:       make([]string, 1),
		UpdateInterval: time.Minute,
	}
	err := r.Start()
	if err != nil {
		t.Fatal(err)
	}
	s.Plugins.Add(r)

	s.RegisterName("Arith", new(Arith), "")
	go s.Serve("tcp", "127.0.0.1:8972")
	defer s.Close()

	if len(r.Services) != 1 {
		t.Fatal("failed to register services in consul")
	}

}
