package client

import (
	"context"
	"testing"
	"time"

	"github.com/caser789/rpcj/_testutils"
	"github.com/caser789/rpcj/protocol"
	"github.com/caser789/rpcj/server"
)

type Args struct {
	A int
	B int
}

type Reply struct {
	C int
}

type Arith int

func (t *Arith) Mul(ctx context.Context, args *Args, reply *Reply) error {
	reply.C = args.A * args.B
	return nil
}

type PBArith int

func (t *PBArith) Mul(ctx context.Context, args *testutils.ProtoArgs, reply *testutils.ProtoReply) error {
	reply.C = args.A * args.B
	return nil
}

func TestClient_IT(t *testing.T) {
	s := server.Server{}
	s.RegisterName("Arith", new(Arith), "")
	s.RegisterName("PBArith", new(PBArith), "")
	go s.Serve("tcp", "127.0.0.1:0")
	defer s.Close()
	time.Sleep(500 * time.Millisecond)

	addr := s.Address().String()

	client := &Client{
		option: DefaultOption,
	}

	err := client.Connect("tcp", addr)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	args := &Args{
		A: 10,
		B: 20,
	}

	reply := &Reply{}
	err = client.Call(context.Background(), "Arith", "Mul", args, reply, nil)
	if err != nil {
		t.Fatalf("failed to call: %v", err)
	}

	if reply.C != 200 {
		t.Fatalf("expect 200 but got %d", reply.C)
	}

	err = client.Call(context.Background(), "Arith", "Add", args, reply, nil)
	if err == nil {
		t.Fatal("expect an error but got nil")
	}

	client.option.SerializeType = protocol.MsgPack
	reply = &Reply{}
	err = client.Call(context.Background(), "Arith", "Mul", args, reply, nil)
	if err != nil {
		t.Fatalf("failed to call: %v", err)
	}

	if reply.C != 200 {
		t.Fatalf("expect 200 but got %d", reply.C)
	}

	client.option.SerializeType = protocol.ProtoBuffer

	pbArgs := &testutils.ProtoArgs{
		A: 10,
		B: 20,
	}
	pbReply := &testutils.ProtoReply{}
	err = client.Call(context.Background(), "PBArith", "Mul", pbArgs, pbReply, nil)
	if err != nil {
		t.Fatalf("failed to call: %v", err)
	}

	if pbReply.C != 200 {
		t.Fatalf("expect 200 but got %d", pbReply.C)
	}
}
