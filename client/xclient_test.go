package client

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/caser789/rpcj/_testutils"
	"github.com/caser789/rpcj/protocol"
	"github.com/caser789/rpcj/server"
	"github.com/caser789/rpcj/share"
)

func TestLoop(t *testing.T) {
	opt := Option{
		Retries:        1,
		RPCPath:        share.DefaultRPCPath,
		ConnectTimeout: 10 * time.Second,
		SerializeType:  protocol.Thrift,
		CompressType:   protocol.None,
		BackupLatency:  10 * time.Millisecond,
	}

	d := NewPeer2PeerDiscovery("tcp@127.0.0.1:8995", "desc=a test service")
	xclient := NewXClient("Arith", Failtry, RandomSelect, d, opt)

	defer xclient.Close()

	tick := time.NewTicker(2 * time.Second)
	for ti := range tick.C {
		fmt.Println(ti)
		args := testutils.ThriftArgs_{}
		args.A = 200
		args.B = 100
		go func() {
			reply := testutils.ThriftReply{}
			err := xclient.Call(context.Background(), "ThriftMul", &args, &reply)
			fmt.Println(reply.C, err)
		}()
	}

}

func TestXClient_Thrift(t *testing.T) {
	opt := Option{
		Retries:        1,
		RPCPath:        share.DefaultRPCPath,
		ConnectTimeout: 10 * time.Second,
		SerializeType:  protocol.Thrift,
		CompressType:   protocol.None,
		BackupLatency:  10 * time.Millisecond,
	}

	d := NewPeer2PeerDiscovery("tcp@127.0.0.1:8999", "desc=a test service")
	xclient := NewXClient("Arith", Failtry, RandomSelect, d, opt)

	defer xclient.Close()

	args := testutils.ThriftArgs_{}
	args.A = 200
	args.B = 100

	reply := testutils.ThriftReply{}

	err := xclient.Call(context.Background(), "ThriftMul", &args, &reply)
	if err != nil {
		t.Fatalf("failed to call: %v", err)
	}

	fmt.Println(reply.C)
	if reply.C != 20000 {
		t.Fatalf("expect 20000 but got %d", reply.C)
	}
}

func TestXClient_IT(t *testing.T) {
	s := server.NewServer()
	s.RegisterName("Arith", new(Arith), "")
	go s.Serve("tcp", "127.0.0.1:0")
	defer s.Close()
	time.Sleep(500 * time.Millisecond)

	addr := s.Address().String()

	d := NewPeer2PeerDiscovery("tcp@"+addr, "desc=a test service")
	xclient := NewXClient("Arith", Failtry, RandomSelect, d, DefaultOption)

	defer xclient.Close()

	args := &Args{
		A: 10,
		B: 20,
	}

	reply := &Reply{}
	err := xclient.Call(context.Background(), "Mul", args, reply)
	if err != nil {
		t.Fatalf("failed to call: %v", err)
	}

	if reply.C != 200 {
		t.Fatalf("expect 200 but got %d", reply.C)
	}
}

func TestXClient_filterByStateAndGroup(t *testing.T) {
	servers := map[string]string{"a": "", "b": "state=inactive&ops=10", "c": "ops=20", "d": "group=test&ops=20"}
	filterByStateAndGroup("test", servers)
	if _, ok := servers["b"]; ok {
		t.Error("has not remove inactive node")
	}
	if _, ok := servers["a"]; ok {
		t.Error("has not remove inactive node")
	}
	if _, ok := servers["c"]; ok {
		t.Error("has not remove inactive node")
	}
	if _, ok := servers["d"]; !ok {
		t.Error("node must be removed")
	}
}
